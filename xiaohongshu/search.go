package xiaohongshu

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
)

type SearchResult struct {
	Search struct {
		Feeds FeedsValue `json:"feeds"`
	} `json:"search"`
}

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// internalFilterOption 内部使用的筛选选项(基于索引)
type internalFilterOption struct {
	FiltersIndex int    // 筛选组索引
	TagsIndex    int    // 标签索引
	Text         string // 标签文本描述
}

const (
	searchOperationTimeout   = 45 * time.Second
	filterInteractionTimeout = 5 * time.Second
)

// 预定义的筛选选项映射表（内部使用）
var filterOptionsMap = map[int][]internalFilterOption{
	1: { // 排序依据
		{FiltersIndex: 1, TagsIndex: 1, Text: "综合"},
		{FiltersIndex: 1, TagsIndex: 2, Text: "最新"},
		{FiltersIndex: 1, TagsIndex: 3, Text: "最多点赞"},
		{FiltersIndex: 1, TagsIndex: 4, Text: "最多评论"},
		{FiltersIndex: 1, TagsIndex: 5, Text: "最多收藏"},
	},
	2: { // 笔记类型
		{FiltersIndex: 2, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 2, TagsIndex: 2, Text: "视频"},
		{FiltersIndex: 2, TagsIndex: 3, Text: "图文"},
	},
	3: { // 发布时间
		{FiltersIndex: 3, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 3, TagsIndex: 2, Text: "一天内"},
		{FiltersIndex: 3, TagsIndex: 3, Text: "一周内"},
		{FiltersIndex: 3, TagsIndex: 4, Text: "半年内"},
	},
	4: { // 搜索范围
		{FiltersIndex: 4, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 4, TagsIndex: 2, Text: "已看过"},
		{FiltersIndex: 4, TagsIndex: 3, Text: "未看过"},
		{FiltersIndex: 4, TagsIndex: 4, Text: "已关注"},
	},
	5: { // 位置距离
		{FiltersIndex: 5, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 5, TagsIndex: 2, Text: "同城"},
		{FiltersIndex: 5, TagsIndex: 3, Text: "附近"},
	},
}

// convertToInternalFilters 将 FilterOption 转换为内部的 internalFilterOption 列表
func convertToInternalFilters(filter FilterOption) ([]internalFilterOption, error) {
	var internalFilters []internalFilterOption

	// 处理排序依据
	if filter.SortBy != "" && filter.SortBy != "综合" {
		internal, err := findInternalOption(1, filter.SortBy)
		if err != nil {
			return nil, fmt.Errorf("排序依据错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理笔记类型
	if filter.NoteType != "" && filter.NoteType != "不限" {
		internal, err := findInternalOption(2, filter.NoteType)
		if err != nil {
			return nil, fmt.Errorf("笔记类型错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理发布时间
	if filter.PublishTime != "" && filter.PublishTime != "不限" {
		internal, err := findInternalOption(3, filter.PublishTime)
		if err != nil {
			return nil, fmt.Errorf("发布时间错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理搜索范围
	if filter.SearchScope != "" && filter.SearchScope != "不限" {
		internal, err := findInternalOption(4, filter.SearchScope)
		if err != nil {
			return nil, fmt.Errorf("搜索范围错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理位置距离
	if filter.Location != "" && filter.Location != "不限" {
		internal, err := findInternalOption(5, filter.Location)
		if err != nil {
			return nil, fmt.Errorf("位置距离错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	return internalFilters, nil
}

// findInternalOption 根据筛选组索引和文本查找内部筛选选项
func findInternalOption(filtersIndex int, text string) (internalFilterOption, error) {
	options, exists := filterOptionsMap[filtersIndex]
	if !exists {
		return internalFilterOption{}, fmt.Errorf("筛选组 %d 不存在", filtersIndex)
	}

	for _, option := range options {
		if option.Text == text {
			return option, nil
		}
	}

	return internalFilterOption{}, fmt.Errorf("在筛选组 %d 中未找到文本 '%s'", filtersIndex, text)
}

// validateInternalFilterOption 验证内部筛选选项是否在有效范围内
func validateInternalFilterOption(filter internalFilterOption) error {
	// 检查筛选组索引是否有效
	if filter.FiltersIndex < 1 || filter.FiltersIndex > 5 {
		return fmt.Errorf("无效的筛选组索引 %d，有效范围为 1-5", filter.FiltersIndex)
	}

	// 检查标签索引是否在对应筛选组的有效范围内
	options, exists := filterOptionsMap[filter.FiltersIndex]
	if !exists {
		return fmt.Errorf("筛选组 %d 不存在", filter.FiltersIndex)
	}

	if filter.TagsIndex < 1 || filter.TagsIndex > len(options) {
		return fmt.Errorf("筛选组 %d 的标签索引 %d 超出范围，有效范围为 1-%d",
			filter.FiltersIndex, filter.TagsIndex, len(options))
	}

	return nil
}

type SearchAction struct {
	page *rod.Page
}

// conciseRodError unwraps rod.TryError without including the captured goroutine
// stack in expected timeout logs and API responses.
func conciseRodError(err error) error {
	var tryErr *rod.TryError
	if stderrors.As(err, &tryErr) {
		if cause, ok := tryErr.Value.(error); ok {
			return cause
		}
		return fmt.Errorf("%v", tryErr.Value)
	}
	return err
}

func securityVerificationReason(bodyText string) string {
	if strings.Contains(bodyText, "Requests too frequent") || strings.Contains(bodyText, "请求过于频繁") {
		return "请求过于频繁，请停止重试并稍后再试"
	}
	return "需要使用已登录的小红书 App 完成扫码安全验证"
}

func NewSearchAction(page *rod.Page) *SearchAction {
	return &SearchAction{page: page}
}

// waitForFeedsSettled 等待搜索结果异步数据真正加载完成。
// window.__INITIAL_STATE__ 在页面刚初始化时就已存在（feeds 是空数组占位），
// 直接判断它 !== undefined 会在异步请求返回之前读到"假的空结果"。
// 这里改为轮询等待，直到 feeds 数组非空、或明确出现登录墙/笔记链接等可判定信号；
// 超时（8秒）后放弃等待，按当前状态继续——此时才认为是真实的零结果。
func waitForFeedsSettled(page *rod.Page, timeout time.Duration) error {
	settledJS := `() => {
		if (location.pathname.includes('/website-login/captcha')) return true;
		if (document.title === 'Security Verification') return true;
		if (window.__INITIAL_STATE__ === undefined) return false;
		if (document.body && document.body.innerText.includes('登录后查看搜索结果')) return true;
		const search = window.__INITIAL_STATE__.search;
		if (!search) return false;
		const feeds = search.feeds;
		const raw = feeds ? (feeds.value !== undefined ? feeds.value : feeds._value) : undefined;
		if (Array.isArray(raw) && raw.length > 0) return true;
		return document.querySelectorAll('a[href*="/explore/"]').length > 0;
	}`

	return rod.Try(func() {
		page.Timeout(timeout).MustWait(settledJS)
	})
}

func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	start := time.Now()
	searchCtx, cancel := context.WithTimeout(ctx, searchOperationTimeout)
	defer cancel()
	page := s.page.Context(searchCtx)

	// 直接打开搜索结果页比“首页输入框 + Enter”的浏览器交互更稳定。
	// xiaohongshu SPA 的搜索输入框有时存在但暂不可交互，会卡在 SelectAll/Input；
	// 直接 URL 搭配 waitForFeedsSettled 既能避免旧版空数组竞态，也能规避输入框遮罩/焦点问题。
	searchURL := makeSearchURL(keyword)
	logrus.Infof("搜索Feeds: 打开搜索结果页 keyword=%q", keyword)
	if err := rod.Try(func() {
		page.Timeout(20 * time.Second).MustNavigate(searchURL).MustWaitLoad()
	}); err != nil {
		return nil, fmt.Errorf("打开搜索结果页失败: %w", conciseRodError(err))
	}
	logrus.Infof("搜索Feeds: 页面加载完成 elapsed=%s", time.Since(start).Round(time.Millisecond))

	if err := waitForFeedsSettled(page, 10*time.Second); err != nil {
		logrus.Warnf("搜索Feeds: 等待搜索结果完成超时，继续读取当前页面状态: %v", conciseRodError(err))
	} else {
		logrus.Infof("搜索Feeds: 搜索结果已就绪 elapsed=%s", time.Since(start).Round(time.Millisecond))
	}

	// 必须先识别登录墙和安全验证，再尝试操作筛选 DOM。否则验证码页面上
	// 不存在 div.filter，旧逻辑会一直等待元素并长期占用 Chromium。
	logrus.Infof("搜索Feeds: 开始读取页面状态 elapsed=%s", time.Since(start).Round(time.Millisecond))
	var pageState string
	if err := rod.Try(func() {
		pageState = page.Timeout(5 * time.Second).MustEval(`() => JSON.stringify({
			bodyText: document.body ? document.body.innerText.slice(0, 500) : "",
			hasLoginGate: document.body ? document.body.innerText.includes("登录后查看搜索结果") : false,
			isSecurityVerification: location.pathname.includes("/website-login/captcha") || document.title === "Security Verification",
			title: document.title,
			pathname: location.pathname,
			url: location.href,
		})`).String()
	}); err != nil {
		return nil, fmt.Errorf("读取搜索页状态超时或失败: %w", conciseRodError(err))
	}
	logrus.Infof("搜索Feeds: 页面状态读取完成 bytes=%d elapsed=%s", len(pageState), time.Since(start).Round(time.Millisecond))

	if pageState != "" {
		var state struct {
			BodyText               string `json:"bodyText"`
			HasLoginGate           bool   `json:"hasLoginGate"`
			IsSecurityVerification bool   `json:"isSecurityVerification"`
			Title                  string `json:"title"`
			Pathname               string `json:"pathname"`
			URL                    string `json:"url"`
		}
		if err := json.Unmarshal([]byte(pageState), &state); err != nil {
			return nil, fmt.Errorf("解析搜索页状态失败: %w", err)
		}
		if state.IsSecurityVerification {
			return nil, fmt.Errorf("搜索触发小红书安全验证（%s）: %s", securityVerificationReason(state.BodyText), state.URL)
		}
		if state.HasLoginGate {
			return nil, fmt.Errorf("搜索页未进入可见结果态，当前页面提示需要登录查看搜索结果: %s", state.URL)
		}
	}

	// 将所有 FilterOption 转换为内部筛选选项
	var allInternalFilters []internalFilterOption
	for _, filter := range filters {
		internalFilters, err := convertToInternalFilters(filter)
		if err != nil {
			return nil, fmt.Errorf("筛选选项转换失败: %w", err)
		}
		allInternalFilters = append(allInternalFilters, internalFilters...)
	}

	// 只有存在有效筛选项时才操作筛选面板；空筛选对象会导致页面一直等待弹层。
	if len(allInternalFilters) > 0 {
		// 验证所有内部筛选选项
		for _, filter := range allInternalFilters {
			if err := validateInternalFilterOption(filter); err != nil {
				return nil, fmt.Errorf("筛选选项验证失败: %w", err)
			}
		}

		// 页面结构或风控状态变化时筛选按钮可能不存在。所有 DOM 操作都必须
		// 使用独立短超时，不能让一个筛选项永久占用 Chromium 执行槽。
		if err := rod.Try(func() {
			filterPage := page.Timeout(filterInteractionTimeout)
			filterPage.MustElement(`div.filter`).MustHover()
			filterPage.MustWait(`() => document.querySelector('div.filter-panel') !== null`)
		}); err != nil {
			return nil, fmt.Errorf("打开搜索筛选面板超时或失败: %w", conciseRodError(err))
		}

		// 应用所有筛选条件
		for _, filter := range allInternalFilters {
			selector := fmt.Sprintf(`div.filter-panel div.filters:nth-child(%d) div.tags:nth-child(%d)`,
				filter.FiltersIndex, filter.TagsIndex)
			if err := rod.Try(func() {
				page.Timeout(filterInteractionTimeout).MustElement(selector).MustClick()
			}); err != nil {
				return nil, fmt.Errorf("应用筛选项 %q 超时或失败: %w", filter.Text, conciseRodError(err))
			}
		}

		// 搜索页会持续请求推荐流，等待 stable 容易卡死；这里只等筛选后的状态回填。
		if err := rod.Try(func() { page.Timeout(10 * time.Second).MustWaitLoad() }); err != nil {
			logrus.Warnf("搜索Feeds: 筛选后等待页面 load 超时，继续读取当前页面状态: %v", conciseRodError(err))
		}
		if err := waitForFeedsSettled(page, 8*time.Second); err != nil {
			logrus.Warnf("搜索Feeds: 筛选后等待搜索结果完成超时，继续读取当前页面状态: %v", conciseRodError(err))
		}
	}

	logrus.Infof("搜索Feeds: 开始提取 feeds JSON elapsed=%s", time.Since(start).Round(time.Millisecond))
	var result string
	if err := rod.Try(func() {
		result = page.Timeout(5 * time.Second).MustEval(`() => {
			if (window.__INITIAL_STATE__ &&
			    window.__INITIAL_STATE__.search &&
			    window.__INITIAL_STATE__.search.feeds) {
				const feeds = window.__INITIAL_STATE__.search.feeds;
				const feedsData = feeds.value !== undefined ? feeds.value : feeds._value;
				if (feedsData) {
					return JSON.stringify(feedsData);
				}
			}
			return "";
		}`).String()
	}); err != nil {
		return nil, fmt.Errorf("提取搜索结果超时或失败: %w", conciseRodError(err))
	}
	logrus.Infof("搜索Feeds: feeds JSON 提取完成 bytes=%d elapsed=%s", len(result), time.Since(start).Round(time.Millisecond))

	if result == "" {
		return nil, errors.ErrNoFeeds
	}

	logrus.Infof("搜索Feeds: 开始反序列化 feeds elapsed=%s", time.Since(start).Round(time.Millisecond))
	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}
	logrus.Infof("搜索Feeds: 反序列化完成 feeds=%d elapsed=%s", len(feeds), time.Since(start).Round(time.Millisecond))

	return feeds, nil
}

func makeSearchURL(keyword string) string {

	values := url.Values{}
	values.Set("keyword", keyword)
	values.Set("source", "web_explore_feed")

	//https://www.xiaohongshu.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_search_result_notes
	//https://www.xiaohongshu.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_explore_feed
	return fmt.Sprintf("https://www.xiaohongshu.com/search_result?%s", values.Encode())
}
