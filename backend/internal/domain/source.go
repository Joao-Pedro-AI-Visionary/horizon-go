package domain

import (
	"encoding/json"
	"time"
)

// Source kinds accepted by the sources table.
const (
	KindHackerNews = "hackernews"
	KindReddit     = "reddit"
	KindRSS        = "rss"
	// KindPush sources are never fetched: items are pushed to
	// POST /api/items/ingest.
	KindPush = "push"
)

// SourceConfig is a configured source row. Config holds kind-specific
// settings (see the adapters for the accepted keys).
type SourceConfig struct {
	ID        int64
	Kind      string
	Name      string
	Config    json.RawMessage
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fetch run statuses. FetchSkipped is a non-fatal outcome: the source chose
// not to fetch this time (e.g. the upstream is rate limiting us).
const (
	FetchOK      = "ok"
	FetchError   = "error"
	FetchSkipped = "skipped"
)

// SkippedError is returned by a source that deliberately skipped a fetch.
// The collector records it as FetchSkipped instead of FetchError.
type SkippedError struct {
	Reason     string
	RetryAfter time.Duration
}

func (e *SkippedError) Error() string { return e.Reason }

// FetchRun is the per-source report of one collector run.
type FetchRun struct {
	ID         int64
	RunID      string
	SourceID   int64
	SourceName string
	Status     string
	ItemCount  int
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}
