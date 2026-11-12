package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	return nil
}

func newFake(rpm int) (*Limiter, *fakeClock) {
	c := &fakeClock{t: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	l := NewPerMinute("test", rpm)
	l.Now, l.Sleep = c.now, c.sleep
	return l, c
}

func TestWaitPacesRequests(t *testing.T) {
	l, c := newFake(30) // one start every 2s
	start := c.t
	for range 3 {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.t.Sub(start); got != 4*time.Second {
		t.Errorf("3 starts took %s, want 4s", got)
	}
}

func TestBlockAndMaxWait(t *testing.T) {
	l, c := newFake(0)
	l.Block(10 * time.Second)
	start := c.t
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.t.Sub(start) != 10*time.Second {
		t.Errorf("waited %s, want 10s", c.t.Sub(start))
	}
	l.Block(2 * time.Hour) // e.g. the daily quota is spent
	err := l.Wait(context.Background())
	var le *LimitedError
	if !errors.As(err, &le) || !le.Until.Equal(c.t.Add(2*time.Hour)) {
		t.Fatalf("want LimitedError until +2h, got %v", err)
	}
	if c.t.Sub(start) != 10*time.Second {
		t.Error("a long gate must fail fast without sleeping")
	}
}

func TestNilLimiter(t *testing.T) {
	var l *Limiter
	l.Block(time.Hour)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Retry-After", "2")
	if d, ok := RetryAfter(h, nil, now); !ok || d != 2*time.Second {
		t.Errorf("seconds: %s %v", d, ok)
	}
	h.Set("Retry-After", now.Add(30*time.Second).Format(http.TimeFormat))
	if d, ok := RetryAfter(h, nil, now); !ok || d != 30*time.Second {
		t.Errorf("date: %s %v", d, ok)
	}
	body := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"34s"}]}}`)
	if d, ok := RetryAfter(http.Header{}, body, now); !ok || d != 34*time.Second {
		t.Errorf("google RetryInfo: %s %v", d, ok)
	}
	if _, ok := RetryAfter(http.Header{}, []byte(`nope`), now); ok {
		t.Error("no hint must report ok=false")
	}
}
