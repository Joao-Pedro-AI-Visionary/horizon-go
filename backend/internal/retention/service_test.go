package retention

import (
	"context"
	"testing"
	"time"
)

type recordingStore struct {
	params Params
}

func (s *recordingStore) Cleanup(_ context.Context, p Params) (Counts, error) {
	s.params = p
	return Counts{"items": 3}, nil
}

func TestServiceUsesCalendarCutoffAndForwardsDryRun(t *testing.T) {
	loc, err := time.LoadLocation(Timezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 9, 33, 0, 0, loc)
	store := &recordingStore{}
	svc := Service{Store: store, Now: func() time.Time { return now }}

	report, err := svc.Cleanup(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	wantCutoff := now.AddDate(0, 0, -RetentionDays)
	wantCutoffDate := time.Date(wantCutoff.Year(), wantCutoff.Month(), wantCutoff.Day(), 0, 0, 0, 0, loc)
	if !report.DryRun || report.RetentionDays != RetentionDays || report.Timezone != Timezone ||
		!report.Now.Equal(now) || !report.Cutoff.Equal(wantCutoff) ||
		!store.params.Cutoff.Equal(wantCutoff) || !store.params.CutoffDate.Equal(wantCutoffDate) || !store.params.DryRun {
		t.Fatalf("report=%+v params=%+v", report, store.params)
	}
}
