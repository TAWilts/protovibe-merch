package api

import (
	"testing"
	"time"
)

func TestRegistrationStyleRateLimitResetsAfterWindow(t *testing.T) {
	limiter := newRequestLimiter(3, time.Hour)
	now := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	for attempt := 1; attempt <= 3; attempt++ {
		if allowed, _ := limiter.allow("127.0.0.1", now); !allowed {
			t.Fatalf("attempt %d should be allowed", attempt)
		}
	}
	if allowed, retry := limiter.allow("127.0.0.1", now); allowed || retry <= 0 {
		t.Fatalf("fourth attempt should be limited with a retry delay, got allowed=%v retry=%s", allowed, retry)
	}
	if allowed, _ := limiter.allow("127.0.0.1", now.Add(time.Hour)); !allowed {
		t.Fatal("the bucket should reset after its fixed window")
	}
}
