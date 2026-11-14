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
