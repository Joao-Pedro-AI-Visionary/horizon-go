// Package ratelimit paces calls to hosted model APIs with free-tier quotas
// (Groq, Gemini): a minimum interval between requests, a "not before" gate fed
// by 429 Retry-After and token-budget headers, and bounded waits so a
// long block (a daily quota) fails fast instead of hanging a worker.
package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults for hosted free tiers.
const (
	// DefaultMaxWait is the longest single wait before a call; a provider
	// asking for more (e.g. the daily quota is spent) gets ErrLimited back.
	DefaultMaxWait = 60 * time.Second
	// DefaultRetries is how many 429s one call absorbs before giving up.
	DefaultRetries = 3
	// fallbackWait is used on a 429 without any retry hint.
	fallbackWait = 5 * time.Second
)

// LimitedError means the provider asked us to wait longer than MaxWait. It is
// retryable later (the NATS redelivery / database backfill picks the item up).
type LimitedError struct {
	Provider string
	Until    time.Time
}

func (e *LimitedError) Error() string {
	return fmt.Sprintf("%s: rate limited until %s", e.Provider, e.Until.Format(time.RFC3339))
}

// Limiter is safe for concurrent use. The zero value does not pace.
type Limiter struct {
	Provider string
	// MinInterval spaces request starts (60s / RPM). 0 disables pacing.
	MinInterval time.Duration
	// MaxWait bounds a single wait (default DefaultMaxWait).
	MaxWait time.Duration
	// Now and Sleep are for tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	next      time.Time // earliest start of the next request (pacing)
	notBefore time.Time // provider-imposed gate (429 / token budget)
}

// NewPerMinute returns a limiter allowing rpm request starts per minute.
func NewPerMinute(provider string, rpm int) *Limiter {
	l := &Limiter{Provider: provider}
	if rpm > 0 {
		l.MinInterval = time.Minute / time.Duration(rpm)
	}
	return l
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Limiter) maxWait() time.Duration {
	if l.MaxWait > 0 {
		return l.MaxWait
	}
	return DefaultMaxWait
}

func (l *Limiter) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if l.Sleep != nil {
		return l.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Wait blocks until the next request may start. It returns *LimitedError
// without waiting when the gate is further away than MaxWait.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := l.now()
	start := now
	if l.notBefore.After(start) {
		start = l.notBefore
	}
	if l.next.After(start) {
		start = l.next
	}
	if d := start.Sub(now); d > l.maxWait() {
		l.mu.Unlock()
		return &LimitedError{Provider: l.Provider, Until: start}
	}
	l.next = start.Add(l.MinInterval)
	l.mu.Unlock()
	return l.sleep(ctx, start.Sub(now))
}

// Block keeps every request from starting before now+d.
func (l *Limiter) Block(d time.Duration) {
	if l == nil || d <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.now().Add(d); t.After(l.notBefore) {
		l.notBefore = t
	}
}

// RetryAfter reads the wait a 429/503 asks for: the Retry-After header
// (seconds or HTTP date) or, for Google APIs, the RetryInfo retryDelay in the
// JSON error body. ok is false when there is no hint.
func RetryAfter(h http.Header, body []byte, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
			return time.Duration(s * float64(time.Second)), true
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(t.Sub(now), 0), true
		}
	}
	var g struct {
		Error struct {
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &g) == nil {
		for _, d := range g.Error.Details {
			if strings.HasSuffix(d.Type, "RetryInfo") && d.RetryDelay != "" {
				if dur, err := time.ParseDuration(d.RetryDelay); err == nil && dur >= 0 {
					return dur, true
				}
			}
		}
	}
	return 0, false
}

// FallbackWait is the wait used for a 429 without hints.
func FallbackWait() time.Duration { return fallbackWait }
