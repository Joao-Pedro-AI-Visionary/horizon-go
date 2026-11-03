// Package hn is the Hacker News source adapter (Firebase API:
// topstories.json + item/<id>.json).
package hn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jpdev/horizon-go/internal/adapters/source/internal/textutil"
	"github.com/jpdev/horizon-go/internal/domain"
)

// DefaultBaseURL is the public Firebase endpoint.
const DefaultBaseURL = "https://hacker-news.firebaseio.com/v0"

const (
	defaultMinScore  = 100
	defaultLimit     = 30
	itemConcurrency  = 8
	maxContentRunes  = 1500
	discussionURLFmt = "https://news.ycombinator.com/item?id=%d"
	userAgent        = "horizon-go/0.1 (+https://github.com/Thysrael/Horizon port by JpDev)"
)

// Config is the JSON stored in sources.config for kind=hackernews.
type Config struct {
	MinScore *int `json:"min_score,omitempty"`
	Limit    int  `json:"limit,omitempty"`
}

// Params builds a Source. BaseURL and Client are optional.
type Params struct {
	SourceID int64
	Name     string
	BaseURL  string
	Client   *http.Client
	Config   Config
}

// Source fetches top stories with score >= min_score.
type Source struct {
	sourceID int64
	name     string
	base     string
	client   *http.Client
	minScore int
	limit    int
}

// New returns a configured HN source.
func New(p Params) *Source {
	s := &Source{sourceID: p.SourceID, name: p.Name, base: p.BaseURL, client: p.Client, minScore: defaultMinScore, limit: p.Config.Limit}
	if s.base == "" {
		s.base = DefaultBaseURL
	}
	if s.client == nil {
		s.client = http.DefaultClient
	}
	if p.Config.MinScore != nil {
		s.minScore = *p.Config.MinScore
	}
	if s.limit <= 0 {
		s.limit = defaultLimit
	}
	return s
}

// Name implements ports.Source.
func (s *Source) Name() string { return s.name }

type story struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	By      string `json:"by"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Text    string `json:"text"`
	Score   int    `json:"score"`
	Time    int64  `json:"time"`
	Deleted bool   `json:"deleted"`
	Dead    bool   `json:"dead"`
}

// Fetch implements ports.Source. Individual item failures are skipped; the
// call fails only if the story list fails or every item request fails.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]domain.Item, error) {
	var ids []int64
	if err := s.getJSON(ctx, s.base+"/topstories.json", &ids); err != nil {
		return nil, fmt.Errorf("hn topstories: %w", err)
	}
	if len(ids) > s.limit {
		ids = ids[:s.limit]
	}

	var (
		mu       sync.Mutex
		items    []domain.Item
		failures int
		lastErr  error
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(itemConcurrency)
	fetchedAt := time.Now().UTC()
	for _, id := range ids {
		g.Go(func() error {
			var st story
			if err := s.getJSON(gctx, s.base+"/item/"+strconv.FormatInt(id, 10)+".json", &st); err != nil {
				mu.Lock()
				failures++
				lastErr = err
				mu.Unlock()
				return nil
			}
			it, ok := s.toItem(st, since, fetchedAt)
			if ok {
				mu.Lock()
				items = append(items, it)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 && failures == len(ids) {
		return nil, fmt.Errorf("hn: all %d item requests failed: %w", failures, lastErr)
	}
	return items, nil
}

func (s *Source) toItem(st story, since, fetchedAt time.Time) (domain.Item, bool) {
	if st.Type != "story" || st.Deleted || st.Dead || st.Title == "" {
		return domain.Item{}, false
	}
	published := time.Unix(st.Time, 0).UTC()
	if st.Score < s.minScore || published.Before(since) {
		return domain.Item{}, false
	}
	u := st.URL
	if u == "" {
		u = fmt.Sprintf(discussionURLFmt, st.ID)
	}
	return domain.Item{
		SourceID:    s.sourceID,
		ExternalID:  strconv.FormatInt(st.ID, 10),
		URL:         u,
		Title:       st.Title,
		Content:     textutil.Truncate(textutil.StripHTML(st.Text), maxContentRunes),
		Author:      st.By,
		PublishedAt: published,
		FetchedAt:   fetchedAt,
	}, true
}

func (s *Source) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("GET %s: decode: %w", url, err)
	}
	return nil
}
