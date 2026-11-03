package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDoStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := Do(ctx, Policy{Attempts: 5, Base: time.Second, Max: time.Second}, func(context.Context) error {
		calls++
		return errors.New("transient")
	})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want stop after first call on cancelled ctx", err, calls)
	}
}

func TestDoRetriesUntilSuccess(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{Attempts: 4, Base: time.Millisecond, Max: 2 * time.Millisecond}, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want success on 3rd call", err, calls)
	}
}

func TestDoDoesNotRetryPermanent(t *testing.T) {
	calls := 0
	base := errors.New("bad request")
	err := Do(context.Background(), Policy{Attempts: 4, Base: time.Millisecond}, func(context.Context) error {
		calls++
		return Permanent(base)
	})
	if calls != 1 || !errors.Is(err, base) || !IsPermanent(err) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
