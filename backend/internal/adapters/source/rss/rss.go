// Package rss is the RSS/Atom source adapter (parsed with gofeed).
package rss

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/jpdev/horizon-go/internal/adapters/source/internal/textutil"
	"github.com/jpdev/horizon-go/internal/domain"
)

const (
	userAgent       = "horizon-go/0.1 (+https://github.com/Thysrael/Horizon port by JpDev)"
	maxBodyBytes    = 10 << 20
	maxContentRunes = 1500
)

// Config is the JSON stored in sources.config for kind=rss.
type Config struct {
	URL string `json:"url"`
}

// Validate checks that URL is an absolute http(s) URL.
func (c Config) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("rss: invalid feed url %q", c.URL)
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

// Fetch implements ports.Source. Entries without link or date are skipped.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]domain.Item, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rss GET %s: status %d", s.cfg.URL, resp.StatusCode)
	}
	feed, err := gofeed.NewParser().Parse(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("rss parse %s: %w", s.cfg.URL, err)
	}

	fetchedAt := time.Now().UTC()
	var items []domain.Item
	for _, e := range feed.Items {
		published := e.PublishedParsed
		if published == nil {
			published = e.UpdatedParsed
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
	return items, nil
}
