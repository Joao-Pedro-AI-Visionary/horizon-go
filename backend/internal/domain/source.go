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

// Fetch run statuses.
const (
	FetchOK    = "ok"
	FetchError = "error"
)

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
