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

func TestCanDeleteSafetyGuards(t *testing.T) {
	cases := []struct {
		name       string
		counts     Counts
		wantAllow  bool
		wantReason string
	}{
		{"zero items", Counts{"items": 0, "items_total": 100, "feed_view": 0}, false, "no_candidate_items"},
		{"negative items", Counts{"items": -1, "items_total": 100, "feed_view": 0}, false, "no_candidate_items"},
		{"empty corpus", Counts{"items": 1, "items_total": 0, "feed_view": 0}, false, "empty_corpus"},
		{"all items", Counts{"items": 10, "items_total": 10, "feed_view": 5}, false, "candidates_ge_total"},
		{"items gt total", Counts{"items": 11, "items_total": 10, "feed_view": 5}, false, "candidates_ge_total"},
		{"over 80 percent", Counts{"items": 81, "items_total": 100, "feed_view": 10}, false, "candidates_gt_80pct"},
		{"over 80 percent small", Counts{"items": 5, "items_total": 6, "feed_view": 1}, false, "candidates_gt_80pct"},
		{"exact 80 percent", Counts{"items": 80, "items_total": 100, "feed_view": 80}, true, "ok"},
		{"feed_view gt items", Counts{"items": 5, "items_total": 100, "feed_view": 6}, false, "feed_view_gt_items"},
		{"negative feed_view", Counts{"items": 5, "items_total": 100, "feed_view": -1}, false, "feed_view_gt_items"},
		{"safe small batch", Counts{"items": 3, "items_total": 10, "feed_view": 2}, true, "ok"},
	}
	for _, tc := range cases {
		got := CanDelete(tc.counts)
		if got.Allow != tc.wantAllow || got.Reason != tc.wantReason {
			t.Errorf("%s: got (allow=%v, reason=%q), want (allow=%v, reason=%q)",
				tc.name, got.Allow, got.Reason, tc.wantAllow, tc.wantReason)
		}
	}
}
