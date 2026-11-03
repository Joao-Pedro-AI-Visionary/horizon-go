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

// Item is a piece of content fetched from a source.
type Item struct {
	ID          int64
	SourceID    int64
	ExternalID  string
	URL         string
	Title       string
	Content     string
	Author      string
	PublishedAt time.Time
	FetchedAt   time.Time
	Score       *float64
	Tags        []string
}

// ValidateScore checks that s is within the rubric bounds.
func ValidateScore(s float64) error {
	if s < MinScore || s > MaxScore {
		return ErrInvalidScore
	}
	return nil
}
