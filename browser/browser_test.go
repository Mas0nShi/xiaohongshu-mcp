package browser

import (
	"context"
	"testing"
	"time"
)

func TestBrowserConcurrencyFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "default", value: "", want: 1},
		{name: "valid", value: "3", want: 3},
		{name: "zero", value: "0", want: 1},
		{name: "invalid", value: "abc", want: 1},
		{name: "capped", value: "99", want: 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := browserConcurrencyFromEnv(tt.value); got != tt.want {
				t.Fatalf("browserConcurrencyFromEnv(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestBrowserLimiterSerializesAndHonorsContext(t *testing.T) {
	limiter := newBrowserLimiter(1)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := limiter.acquire(ctx); err == nil {
		t.Fatal("second acquire unexpectedly succeeded while slot was occupied")
	}

	limiter.release()
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	limiter.release()

	if active := limiter.active.Load(); active != 0 {
		t.Fatalf("active = %d, want 0", active)
	}
	if peak := limiter.peak.Load(); peak != 1 {
		t.Fatalf("peak = %d, want 1", peak)
	}
}
