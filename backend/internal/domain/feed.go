package domain

import (
	"errors"
	"fmt"
	"time"
)

// FeedItem is one row of the feed_view projection (the primary item of a
// cluster).
type FeedItem struct {
	ItemID         int64      `json:"item_id"`
	SourceID       int64      `json:"source_id"`
	SourceKind     string     `json:"source_kind"`
	SourceName     string     `json:"source_name"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	Summary        string     `json:"summary"`
	ScoreReason    string     `json:"score_reason"`
	Score          float64    `json:"score"`
	Tags           []string   `json:"tags"`
	PublishedAt    *time.Time `json:"published_at"`
	ClusterID      *int64     `json:"cluster_id"`
	DuplicateCount int        `json:"duplicate_count"`
	ProjectedAt    time.Time  `json:"projected_at"`
}

// EventTime is published_at, or projected_at when the source gave no date.
func (f FeedItem) EventTime() time.Time {
	if f.PublishedAt != nil {
		return *f.PublishedAt
	}
	return f.ProjectedAt
}

// Feed sort orders.
const (
	SortScore  = "score"
	SortRecent = "recent"
)

// Feed paging bounds.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// FeedQuery filters the feed. Zero values mean "no filter".
type FeedQuery struct {
	SourceKind string
	SourceID   int64
	Tag        string
	MinScore   *float64
	Q          string
	From, To   time.Time
	Sort       string
	Page       int
	PageSize   int
}

// FeedPage is one page of the feed.
type FeedPage struct {
	Items    []FeedItem `json:"items"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

// ClusterInfo describes the cluster an item belongs to.
type ClusterInfo struct {
	ID                   int64  `json:"id"`
	Title                string `json:"title"`
	ItemCount            int    `json:"item_count"`
	RepresentativeItemID *int64 `json:"representative_item_id"`
}

// RelatedItem is another member of the cluster.
type RelatedItem struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	SourceKind  string     `json:"source_kind"`
	SourceName  string     `json:"source_name"`
	Score       *float64   `json:"score"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"published_at"`
	// Similarity is the cosine similarity of the embeddings (null when either is missing).
	Similarity *float64 `json:"similarity"`
}

// ItemDetail is an item with its cluster and duplicates.
type ItemDetail struct {
	ID          int64         `json:"id"`
	SourceID    int64         `json:"source_id"`
	SourceKind  string        `json:"source_kind"`
	SourceName  string        `json:"source_name"`
	Title       string        `json:"title"`
	URL         string        `json:"url"`
	Author      string        `json:"author"`
	Content     string        `json:"content"`
	Status      string        `json:"status"`
	Score       *float64      `json:"score"`
	ScoreReason string        `json:"score_reason"`
	Summary     string        `json:"summary"`
	Tags        []string      `json:"tags"`
	PublishedAt *time.Time    `json:"published_at"`
	FetchedAt   time.Time     `json:"fetched_at"`
	ScoredAt    *time.Time    `json:"scored_at"`
	LastError   string        `json:"last_error"`
	IsPrimary   bool          `json:"is_primary"`
	Cluster     *ClusterInfo  `json:"cluster"`
	Duplicates  []RelatedItem `json:"duplicates"`
}

// Settings are the runtime knobs edited from the dashboard.
type Settings struct {
	ScoreThreshold  float64   `json:"score_threshold"`
	DigestMaxItems  int       `json:"digest_max_items"`
	DigestPerSource int       `json:"digest_per_source"` // 0 = no per-source quota
	DigestCron      string    `json:"digest_cron"`
	Timezone        string    `json:"timezone"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ErrInvalid marks a validation error (HTTP 400).
var ErrInvalid = errors.New("invalid")

// Invalidf returns an error wrapping ErrInvalid.
func Invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate checks the bounds (the cron spec is checked by the scheduler).
func (s Settings) Validate() error {
	switch {
	case s.ScoreThreshold < MinScore || s.ScoreThreshold > MaxScore:
		return Invalidf("score_threshold must be within 0..10")
	case s.DigestMaxItems < 1 || s.DigestMaxItems > 200:
		return Invalidf("digest_max_items must be within 1..200")
	case s.DigestPerSource < 0 || s.DigestPerSource > 200:
		return Invalidf("digest_per_source must be within 0..200")
	case s.Timezone == "":
		return Invalidf("timezone is required")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return Invalidf("timezone %q: %v", s.Timezone, err)
	}
	return nil
}

// Digest is a rendered daily digest.
type Digest struct {
	ID          int64     `json:"id"`
	Date        string    `json:"date"` // YYYY-MM-DD in the settings timezone
	Language    string    `json:"language"`
	Title       string    `json:"title"`
	Markdown    string    `json:"markdown,omitempty"`
	HTML        string    `json:"html,omitempty"`
	ItemIDs     []int64   `json:"item_ids"`
	ItemCount   int       `json:"item_count"`
	Considered  int       `json:"considered"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TokenUsage is the token count of one model call.
type TokenUsage struct {
	Kind             string // "chat" or "embed"
	Model            string
	PromptTokens     int
	CompletionTokens int
}

// TimelineBucket counts the feed rows whose event time falls on one calendar
// day (in the settings timezone).
type TimelineBucket struct {
	Day   string    `json:"day"`   // YYYY-MM-DD
	Start time.Time `json:"start"` // first instant of the day
	Count int       `json:"count"`
}

// FeedTimeline is the per-day histogram of the feed over the last Days days
// (today included). It drives the date scrubber of the dashboard: a range
// [Buckets[i].Start, Buckets[j+1].Start or To) is sent back as from/to.
type FeedTimeline struct {
	Days     int              `json:"days"`
	Timezone string           `json:"timezone"`
	From     time.Time        `json:"from"`
	To       time.Time        `json:"to"` // exclusive: start of tomorrow
	Total    int              `json:"total"`
	Buckets  []TimelineBucket `json:"buckets"`
}

// Timeline bounds (days).
const (
	DefaultTimelineDays = 30
	MaxTimelineDays     = 90
)

// DayStarts returns the start of each of the last `days` calendar days in loc
// (oldest first, today last) and the start of tomorrow.
func DayStarts(now time.Time, loc *time.Location, days int) ([]time.Time, time.Time) {
	y, m, d := now.In(loc).Date()
	starts := make([]time.Time, days)
	for i := range days {
		starts[i] = time.Date(y, m, d-(days-1-i), 0, 0, 0, 0, loc)
	}
	return starts, time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}
