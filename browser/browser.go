package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

const (
	defaultBrowserConcurrency = 1
	maxBrowserConcurrency     = 16
	gracefulCloseTimeout      = 2 * time.Second
	cleanupTimeout            = 2 * time.Second
	defaultUserAgent          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

type browserConfig struct {
	binPath string
}

type Option func(*browserConfig)

func WithBinPath(binPath string) Option {
	return func(c *browserConfig) {
		c.binPath = binPath
	}
}

// Browser owns one rod connection, its launcher process and one limiter slot.
// Close is idempotent and always releases the slot after bounded cleanup.
type Browser struct {
	browser   *rod.Browser
	launcher  *launcher.Launcher
	limiter   *browserLimiter
	closeOnce sync.Once
}

type browserLimiter struct {
	limit  int
	slots  chan struct{}
	active atomic.Int64
	peak   atomic.Int64
}

func newBrowserLimiter(limit int) *browserLimiter {
	if limit < 1 {
		limit = defaultBrowserConcurrency
	}
	return &browserLimiter{
		limit: limit,
		slots: make(chan struct{}, limit),
	}
}

func (l *browserLimiter) acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}:
		active := l.active.Add(1)
		for {
			peak := l.peak.Load()
			if active <= peak || l.peak.CompareAndSwap(peak, active) {
				break
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *browserLimiter) release() {
	<-l.slots
	l.active.Add(-1)
}

func browserConcurrencyFromEnv(value string) int {
	if value == "" {
		return defaultBrowserConcurrency
	}

	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 {
		logrus.Warnf("invalid XHS_BROWSER_CONCURRENCY=%q; using %d", value, defaultBrowserConcurrency)
		return defaultBrowserConcurrency
	}
	if limit > maxBrowserConcurrency {
		logrus.Warnf("XHS_BROWSER_CONCURRENCY=%d exceeds maximum; capped at %d", limit, maxBrowserConcurrency)
		return maxBrowserConcurrency
	}
	return limit
}

var processBrowserLimiter = newBrowserLimiter(browserConcurrencyFromEnv(os.Getenv("XHS_BROWSER_CONCURRENCY")))

// maskProxyCredentials masks username and password in proxy URL for safe logging.
func maskProxyCredentials(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return proxyURL
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword("***", "***")
	} else {
		u.User = url.User("***")
	}
	return u.String()
}

// NewBrowser preserves the original package API for commands and tests that do
// not have a request context.
func NewBrowser(headless bool, options ...Option) *Browser {
	b, err := NewBrowserContext(context.Background(), headless, options...)
	if err != nil {
		panic(err)
	}
	return b
}

// NewBrowserContext starts an isolated browser after acquiring the process-wide
// slot. The default limit is one because a Chromium instance typically consumes
// hundreds of MB. Set XHS_BROWSER_CONCURRENCY to opt into more parallelism.
func NewBrowserContext(ctx context.Context, headless bool, options ...Option) (_ *Browser, err error) {
	cfg := &browserConfig{}
	for _, opt := range options {
		opt(cfg)
	}

	waitStart := time.Now()
	if err := processBrowserLimiter.acquire(ctx); err != nil {
		return nil, fmt.Errorf("等待浏览器执行槽失败: %w", err)
	}
	acquired := true
	defer func() {
		if acquired {
			processBrowserLimiter.release()
		}
	}()

	waited := time.Since(waitStart)
	active := processBrowserLimiter.active.Load()
	if waited >= 100*time.Millisecond {
		logrus.Infof("浏览器并发限流: 获得执行槽 active=%d limit=%d waited=%s", active, processBrowserLimiter.limit, waited.Round(time.Millisecond))
	} else {
		logrus.Debugf("浏览器执行槽: active=%d limit=%d", active, processBrowserLimiter.limit)
	}

	l := launcher.New().
		Headless(headless).
		NoSandbox(true).
		Set("user-agent", defaultUserAgent)

	if cfg.binPath != "" {
		l = l.Bin(cfg.binPath)
	}
	if proxy := os.Getenv("XHS_PROXY"); proxy != "" {
		l = l.Proxy(proxy)
		logrus.Infof("Using proxy: %s", maskProxyCredentials(proxy))
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动 Chromium 失败: %w", err)
	}

	managed := &Browser{
		launcher: l,
		limiter:  processBrowserLimiter,
	}
	acquired = false

	rb := rod.New().ControlURL(controlURL)
	if err := rb.Connect(); err != nil {
		managed.Close()
		return nil, fmt.Errorf("连接 Chromium 失败: %w", err)
	}
	managed.browser = rb

	loadCookies(rb)
	return managed, nil
}

func loadCookies(rb *rod.Browser) {
	cookiePath := cookies.GetCookiesFilePath()
	data, err := cookies.NewLoadCookie(cookiePath).LoadCookies()
	if err != nil {
		logrus.Warnf("failed to load cookies: %v", err)
		return
	}

	var browserCookies []*proto.NetworkCookie
	if err := json.Unmarshal(data, &browserCookies); err != nil {
		logrus.Warnf("failed to unmarshal cookies: %v", err)
		return
	}
	if err := rb.SetCookies(proto.CookiesToParams(browserCookies)); err != nil {
		logrus.Warnf("failed to set cookies: %v", err)
		return
	}
	logrus.Debug("loaded cookies from file successfully")
}

func (b *Browser) NewPage() *rod.Page {
	return stealth.MustPage(b.browser)
}

// Close first asks Chromium to exit, then force-kills the launcher process if
// either the DevTools close request or process cleanup exceeds its deadline.
func (b *Browser) Close() {
	if b == nil {
		return
	}

	b.closeOnce.Do(func() {
		defer b.limiter.release()
		forceKill := b.browser == nil

		if b.browser != nil {
			closeDone := make(chan error, 1)
			go func() {
				closeDone <- b.browser.Timeout(gracefulCloseTimeout).Close()
			}()

			select {
			case err := <-closeDone:
				if err != nil {
					forceKill = true
					logrus.Warnf("Chromium graceful close failed; forcing process exit: %v", err)
				}
			case <-time.After(gracefulCloseTimeout + 250*time.Millisecond):
				forceKill = true
				logrus.Warn("Chromium graceful close timed out; forcing process exit")
			}
		}

		if forceKill {
			b.launcher.Kill()
		}

		cleanupDone := make(chan struct{})
		go func() {
			b.launcher.Cleanup()
			close(cleanupDone)
		}()

		select {
		case <-cleanupDone:
			return
		case <-time.After(cleanupTimeout):
			logrus.Warn("Chromium process cleanup timed out; killing process group")
			b.launcher.Kill()
		}

		select {
		case <-cleanupDone:
		case <-time.After(cleanupTimeout):
			logrus.Errorf("Chromium process did not exit after force kill pid=%d", b.launcher.PID())
		}
	})
}
