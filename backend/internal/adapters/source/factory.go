// Package source builds source adapters (hn, reddit, rss) from configured
// rows of the sources table.
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/source/hn"
	"github.com/jpdev/horizon-go/internal/adapters/source/reddit"
	"github.com/jpdev/horizon-go/internal/adapters/source/rss"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// BaseURLs overrides the public endpoints (used by tests).
type BaseURLs struct {
	HackerNews string
	Reddit     string
}

// Build returns the adapter for sc. Config keys are validated per kind.
func Build(sc domain.SourceConfig, client *http.Client, base BaseURLs) (ports.Source, error) {
	raw := sc.Config
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	switch sc.Kind {
	case domain.KindHackerNews:
		var c hn.Config
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("source %q: hackernews config: %w", sc.Name, err)
		}
		return hn.New(hn.Params{SourceID: sc.ID, Name: sc.Name, BaseURL: base.HackerNews, Client: client, Config: c}), nil
	case domain.KindReddit:
		var c reddit.Config
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("source %q: reddit config: %w", sc.Name, err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("source %q: %w", sc.Name, err)
		}
		return reddit.New(reddit.Params{SourceID: sc.ID, Name: sc.Name, BaseURL: base.Reddit, Client: client, Config: c}), nil
	case domain.KindRSS:
		var c rss.Config
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("source %q: rss config: %w", sc.Name, err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("source %q: %w", sc.Name, err)
		}
		return rss.New(rss.Params{SourceID: sc.ID, Name: sc.Name, Client: client, Config: c}), nil
	case domain.KindPush:
		return pushSource{name: sc.Name}, nil
	default:
		return nil, fmt.Errorf("source %q: unknown kind %q", sc.Name, sc.Kind)
	}
}

// pushSource stands for a kind=push row: it has nothing to fetch, its items
// arrive through POST /api/items/ingest.
type pushSource struct{ name string }

func (p pushSource) Name() string { return p.name }
func (p pushSource) Fetch(context.Context, time.Time) ([]domain.Item, error) {
	return nil, nil
}
