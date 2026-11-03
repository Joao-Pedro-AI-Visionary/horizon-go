// Package retry runs a call with exponential backoff and equal jitter.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Policy is exponential backoff with equal jitter (delay/2 + rand(delay/2)).
type Policy struct {
	Attempts int           // total calls, including the first
	Base     time.Duration // delay before the second call
	Max      time.Duration // delay cap
}

// Default is used for model calls.
var Default = Policy{Attempts: 3, Base: 500 * time.Millisecond, Max: 8 * time.Second}

type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent marks err as not worth retrying (e.g. model not found).
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Do calls fn until it succeeds, returns a permanent error, the attempts
// run out or ctx is done. It returns the last error.
func Do(ctx context.Context, p Policy, fn func(context.Context) error) error {
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	var err error
	for i := 0; i < p.Attempts; i++ {
		if err = fn(ctx); err == nil || IsPermanent(err) {
			return err
		}
		if i == p.Attempts-1 {
			break
		}
		d := p.Base << i
		if p.Max > 0 && (d > p.Max || d <= 0) {
			d = p.Max
		}
		if d > 0 {
			d = d/2 + rand.N(d/2+1)
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
	}
	return err
}
