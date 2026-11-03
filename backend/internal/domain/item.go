package domain

import (
	"errors"
	"time"
)

// Score bounds used by the LLM rubric (0–10).
const (
	MinScore = 0.0
	MaxScore = 10.0
)

// ErrInvalidScore is returned when a score falls outside [MinScore, MaxScore].
var ErrInvalidScore = errors.New("domain: score out of range 0..10")

// Item is a piece of content fetched from a source. The JSON form is the
// payload published on the items.raw subject.
type Item struct {
	ID            int64     `json:"id,omitempty"`
	SourceID      int64     `json:"source_id"`
	ExternalID    string    `json:"external_id"`
	URL           string    `json:"url"`
	URLNormalized string    `json:"url_normalized"`
	URLHash       string    `json:"url_hash"`
	Title         string    `json:"title"`
	Content       string    `json:"content,omitempty"`
	Author        string    `json:"author,omitempty"`
	PublishedAt   time.Time `json:"published_at"`
	FetchedAt     time.Time `json:"fetched_at"`
	Score         *float64  `json:"score,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
}

// Prepare fills URLNormalized and URLHash from URL.
func (it *Item) Prepare() error {
	n, err := NormalizeURL(it.URL)
	if err != nil {
		return err
	}
	it.URLNormalized = n
	it.URLHash = URLHash(n)
	return nil
}

// ValidateScore checks that s is within the rubric bounds.
func ValidateScore(s float64) error {
	if s < MinScore || s > MaxScore {
		return ErrInvalidScore
	}
	return nil
}
