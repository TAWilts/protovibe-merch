package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// requestLimiter is a deliberately small, per-process fixed-window limiter.
// It protects the anonymous authentication surface without adding a second
// infrastructure dependency. Deployments with several API replicas should
// additionally enforce a shared limit at their reverse proxy.
type requestLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]rateLimitEntry
}

type rateLimitEntry struct {
	count int
	reset time.Time
}

func newRequestLimiter(limit int, window time.Duration) *requestLimiter {
	return &requestLimiter{limit: limit, window: window, entries: make(map[string]rateLimitEntry)}
}

func (l *requestLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Public endpoints can see many one-off source addresses. Keep their stale
	// buckets from growing for the lifetime of the process.
	if len(l.entries) > 4096 {
		for candidate, item := range l.entries {
			if !now.Before(item.reset) {
				delete(l.entries, candidate)
			}
		}
	}

	entry := l.entries[key]
	if entry.reset.IsZero() || !now.Before(entry.reset) {
		entry = rateLimitEntry{reset: now.Add(l.window)}
	}
	entry.count++
	l.entries[key] = entry
	if entry.count <= l.limit {
		return true, 0
	}
	return false, entry.reset.Sub(now)
}

// authRateLimit covers every anonymous step that accepts a password, setup
// code or MFA code. The key includes both source address and endpoint so a
// valid multi-step sign-in does not consume one shared bucket.
func (s *Server) authRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		switch path {
		case "/api/v1/auth/login", "/api/v1/auth/mfa", "/api/v1/auth/password-setup",
			"/api/v1/auth/password-reset/request", "/api/v1/auth/password-reset/confirm",
			"/api/v1/mfa/enrollment/start", "/api/v1/mfa/enrollment/confirm":
		default:
			c.Next()
			return
		}

		allowed, retryAfter := s.authLimiter.allow(c.ClientIP()+"|"+path, time.Now())
		if allowed {
			c.Next()
			return
		}
		seconds := int(retryAfter.Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many authentication attempts; try again later")
	}
}
