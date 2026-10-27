package reviews

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults of the AddReview rate limit (per product + author, per process).
const (
	DefaultRateLimit  = 5
	DefaultRateWindow = time.Minute
	// maxLimiterKeys bounds the limiter's memory: with this many distinct
	// product+author keys that are all still inside their window, a new key is
	// refused (fail closed) until some expire.
	maxLimiterKeys = 50_000
)

// RateLimiter is a sliding-window limiter: at most limit events per key in
// any window. It lives in the memory of one process, so with N replicas the
// effective limit is N times higher, and a restart resets it. A limit <= 0
// disables it.
type RateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	events map[string][]time.Time
}

// NewRateLimiter creates a limiter; now is the clock (nil = time.Now).
func NewRateLimiter(limit int, window time.Duration, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{limit: limit, window: window, now: now, events: map[string][]time.Time{}}
}

// Enabled reports whether the limiter limits anything.
func (l *RateLimiter) Enabled() bool { return l != nil && l.limit > 0 && l.window > 0 }

// Allow records an attempt for key and reports whether it is within the
// limit. When it is not, retryAfter says when the oldest attempt leaves the
// window. Rejected attempts are not recorded.
func (l *RateLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	if !l.Enabled() {
		return true, 0
	}
	now := l.now()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := l.events[key]
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.limit {
		l.events[key] = ts
		return false, ts[0].Add(l.window).Sub(now)
	}
	if len(ts) == 0 && len(l.events) >= maxLimiterKeys {
		l.sweep(cutoff)
		if len(l.events) >= maxLimiterKeys {
			return false, l.window
		}
	}
	l.events[key] = append(ts, now)
	return true, 0
}

// sweep drops keys whose events are all outside the window. Caller holds mu.
func (l *RateLimiter) sweep(cutoff time.Time) {
	for k, ts := range l.events {
		if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
			delete(l.events, k)
		}
	}
}

// RateLimitFromEnv reads REVIEW_RATE_LIMIT (reviews per window and per
// product+author, 0 disables; default 5) and REVIEW_RATE_WINDOW (a Go
// duration such as 30s or 2m; default 1m). A value that cannot be parsed
// yields the default for that setting and an error describing it, so a typo
// never silently switches the protection off.
func RateLimitFromEnv(getenv func(string) string) (limit int, window time.Duration, err error) {
	limit, window = DefaultRateLimit, DefaultRateWindow
	var problems []string
	if v := strings.TrimSpace(getenv("REVIEW_RATE_LIMIT")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 0 {
			problems = append(problems, fmt.Sprintf("REVIEW_RATE_LIMIT=%q is not a non-negative integer, using %d", v, DefaultRateLimit))
		} else {
			limit = n
		}
	}
	if v := strings.TrimSpace(getenv("REVIEW_RATE_WINDOW")); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil || d <= 0 {
			problems = append(problems, fmt.Sprintf("REVIEW_RATE_WINDOW=%q is not a positive duration, using %s", v, DefaultRateWindow))
		} else {
			window = d
		}
	}
	if len(problems) > 0 {
		err = fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return limit, window, err
}
