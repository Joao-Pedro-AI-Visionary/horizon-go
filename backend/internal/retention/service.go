// Package retention owns the administrative retention policy for stored news.
package retention

import (
	"context"
	"time"
)

const (
	RetentionDays = 32
	Timezone      = "America/Sao_Paulo"
)

// Params is the immutable input used by the database adapter. CutoffDate is
// kept as a time.Time at midnight in the policy timezone so PostgreSQL can use
// the same calendar boundary for DATE columns and timestamptz columns.
type Params struct {
	Cutoff     time.Time
	CutoffDate time.Time
	DryRun     bool
}

// Counts is keyed by table/effect name to keep the JSON useful for both the
// dry-run audit and the real delete result.
type Counts map[string]int64

// DeleteDecision describes whether a real delete is safe given dry-run counts.
type DeleteDecision struct {
	Allow  bool
	Reason string
	Items  int64
	Total  int64
	Feed   int64
}

// CanDelete checks the retention safety invariants against dry-run counts:
// the candidate set must be non-zero, strictly smaller than items_total,
// at most 80% of items_total, and feed_view candidates must not exceed items.
func CanDelete(counts Counts) DeleteDecision {
	items := counts["items"]
	total := counts["items_total"]
	feed := counts["feed_view"]
	d := DeleteDecision{Items: items, Total: total, Feed: feed}
	switch {
	case items <= 0:
		d.Reason = "no_candidate_items"
	case total <= 0:
		d.Reason = "empty_corpus"
	case items >= total:
		d.Reason = "candidates_ge_total"
	case items > (total*80)/100:
		d.Reason = "candidates_gt_80pct"
	case feed < 0 || feed > items:
		d.Reason = "feed_view_gt_items"
	default:
		d.Allow = true
		d.Reason = "ok"
	}
	return d
}

// Report is the stable JSON contract returned by the cleanup endpoint.
type Report struct {
	DryRun        bool      `json:"dry_run"`
	RetentionDays int       `json:"retention_days"`
	Timezone      string    `json:"timezone"`
	Now           time.Time `json:"now"`
	Cutoff        time.Time `json:"cutoff"`
	Counts        Counts    `json:"counts"`
}

// Store is implemented by the Postgres adapter. A dry-run must only read and
// return the same counts the write transaction would produce.
type Store interface {
	Cleanup(context.Context, Params) (Counts, error)
}

// Service calculates the policy boundary and delegates the database work.
type Service struct {
	Store Store
	Now   func() time.Time
}

func (s Service) Cleanup(ctx context.Context, dryRun bool) (Report, error) {
	loc, err := time.LoadLocation(Timezone)
	if err != nil {
		return Report{}, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	now = now.In(loc)
	cutoff := now.AddDate(0, 0, -RetentionDays)
	cutoffDate := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, loc)
	counts, err := s.Store.Cleanup(ctx, Params{Cutoff: cutoff, CutoffDate: cutoffDate, DryRun: dryRun})
	if err != nil {
		return Report{}, err
	}
	return Report{DryRun: dryRun, RetentionDays: RetentionDays, Timezone: Timezone, Now: now, Cutoff: cutoff, Counts: counts}, nil
}
