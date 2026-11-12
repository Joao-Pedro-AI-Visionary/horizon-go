// Package rss is the RSS/Atom source adapter (parsed with gofeed).
package rss

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/jpdev/horizon-go/internal/adapters/source/internal/textutil"
	"github.com/jpdev/horizon-go/internal/domain"
)

const (
	userAgent       = "horizon-go/0.2 (news radar by JpDev; +https://github.com/Joao-Pedro-AI-Visionary/horizon-go)"
	maxAttempts     = 3
	maxRetryWait    = 20 * time.Second
	maxBodyBytes    = 10 << 20
	maxContentRunes = 1500
)

// MaxLimit bounds Config.Limit.
const MaxLimit = 200

// Config is the JSON stored in sources.config for kind=rss.
//
// Limit (0 = no cap) keeps only the newest N entries of each fetch, so a busy
// feed cannot flood the scorer and its model quota. Undated accepts entries
// without a date (e.g. GitHub trending lists), dated with the feed's own
// date or, failing that, the fetch time.
type Config struct {
	URL     string `json:"url"`
	Limit   int    `json:"limit,omitempty"`
	Undated bool   `json:"undated,omitempty"`
}

// Validate checks that URL is an absolute http(s) URL and Limit is in range.
func (c Config) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("rss: invalid feed url %q", c.URL)
	}
	if c.Limit < 0 || c.Limit > MaxLimit {
		return fmt.Errorf("rss: limit must be within 0..%d", MaxLimit)
	}
	return nil
}

// Params builds a Source. Client is optional.
type Params struct {
	SourceID int64
	Name     string
	Client   *http.Client
	Config   Config
}

// Source fetches one RSS or Atom feed.
type Source struct {
	sourceID int64
	name     string
	client   *http.Client
	cfg      Config
}

// New returns a configured feed source.
func New(p Params) *Source {
	s := &Source{sourceID: p.SourceID, name: p.Name, client: p.Client, cfg: p.Config}
	if s.client == nil {
		s.client = http.DefaultClient
	}
	return s
}

// Name implements ports.Source.
func (s *Source) Name() string { return s.name }

// RetryBase is the first backoff between attempts (tests shorten it).
var RetryBase = 2 * time.Second

func retryable(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// retryAfter reads a Retry-After in seconds, capped; 0 when absent.
func retryAfter(h http.Header) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && n > 0 {
		return min(time.Duration(n)*time.Second, maxRetryWait)
	}
	return 0
}

// get fetches the feed, retrying 429 and 502/503/504 with backoff
// (Retry-After when the server sends one).
func (s *Source) get(ctx context.Context) (*http.Response, error) {
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/atom+xml, application/rss+xml, application/xml;q=0.9, */*;q=0.8")
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		resp.Body.Close()
		last = fmt.Errorf("rss GET %s: status %d", s.cfg.URL, resp.StatusCode)
		if !retryable(resp.StatusCode) || attempt == maxAttempts {
			break
		}
		wait := retryAfter(resp.Header)
		if wait == 0 {
			wait = RetryBase << (attempt - 1)
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(last, ctx.Err())
		case <-time.After(wait):
		}
	}
	return nil, last
}

// Fetch implements ports.Source. Entries without link or date are skipped.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]domain.Item, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	resp, err := s.get(ctx)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	feed, err := gofeed.NewParser().Parse(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("rss parse %s: %w", s.cfg.URL, err)
	}

	fetchedAt := time.Now().UTC()
	feedDate := feed.PublishedParsed
	if feedDate == nil {
		feedDate = feed.UpdatedParsed
	}
	if feedDate == nil || feedDate.After(fetchedAt) {
		feedDate = &fetchedAt
	}
	var items []domain.Item
	for _, e := range feed.Items {
		published := e.PublishedParsed
		if published == nil {
			published = e.UpdatedParsed
		}
		if published == nil && s.cfg.Undated {
			published = feedDate
		}
		if e.Link == "" || e.Title == "" || published == nil || published.Before(since) {
			continue
		}
		id := e.GUID
		if id == "" {
			id = e.Link
		}
		content := e.Content
		if content == "" {
			content = e.Description
		}
		author := ""
		if len(e.Authors) > 0 && e.Authors[0] != nil {
			author = e.Authors[0].Name
		}
		items = append(items, domain.Item{
			SourceID:    s.sourceID,
			ExternalID:  id,
			URL:         e.Link,
			Title:       e.Title,
			Content:     textutil.Truncate(textutil.StripHTML(content), maxContentRunes),
			Author:      author,
			PublishedAt: published.UTC(),
			FetchedAt:   fetchedAt,
		})
	}
	if s.cfg.Limit > 0 && len(items) > s.cfg.Limit {
		sort.SliceStable(items, func(i, j int) bool { return items[i].PublishedAt.After(items[j].PublishedAt) })
		items = items[:s.cfg.Limit]
	}
	return items, nil
}
