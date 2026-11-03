// Package reddit is the Reddit source adapter, using the public listing JSON
// (https://www.reddit.com/r/<sub>/<sort>.json).
package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/source/internal/textutil"
	"github.com/jpdev/horizon-go/internal/domain"
)

// DefaultBaseURL is the public Reddit host.
const DefaultBaseURL = "https://www.reddit.com"

// UserAgent follows Reddit's "<platform>:<app id>:<version> (by ...)" format.
const UserAgent = "server:horizon-go:v0.1 (by JpDev; +https://github.com/Thysrael/Horizon)"

// ErrRateLimited is returned when Reddit keeps answering 429.
var ErrRateLimited = errors.New("reddit: rate limited (429)")

const (
	maxAttempts       = 3
	defaultRetryAfter = 2 * time.Second
	defaultMaxWait    = 30 * time.Second
	defaultLimit      = 25
	maxLimit          = 100
	maxContentRunes   = 1500
)

var (
	subredditRe = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)
	validSorts  = map[string]bool{"hot": true, "new": true, "top": true, "rising": true}
)

// Config is the JSON stored in sources.config for kind=reddit.
type Config struct {
	Subreddit  string `json:"subreddit"`
	Sort       string `json:"sort,omitempty"`
	TimeFilter string `json:"time_filter,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	MinScore   int    `json:"min_score,omitempty"`
}

// Validate checks the subreddit name and sort.
func (c Config) Validate() error {
	if !subredditRe.MatchString(c.Subreddit) {
		return fmt.Errorf("reddit: invalid subreddit %q", c.Subreddit)
	}
	if c.Sort != "" && !validSorts[c.Sort] {
		return fmt.Errorf("reddit: invalid sort %q", c.Sort)
	}
	return nil
}

// Params builds a Source. BaseURL, Client and MaxWait are optional.
type Params struct {
	SourceID int64
	Name     string
	BaseURL  string
	Client   *http.Client
	MaxWait  time.Duration
	Config   Config
}

// Source fetches one subreddit listing.
type Source struct {
	sourceID int64
	name     string
	base     string
	client   *http.Client
	maxWait  time.Duration
	cfg      Config
}

// New returns a configured Reddit source.
func New(p Params) *Source {
	s := &Source{sourceID: p.SourceID, name: p.Name, base: p.BaseURL, client: p.Client, maxWait: p.MaxWait, cfg: p.Config}
	if s.base == "" {
		s.base = DefaultBaseURL
	}
	if s.client == nil {
		s.client = http.DefaultClient
	}
	if s.maxWait <= 0 {
		s.maxWait = defaultMaxWait
	}
	if s.cfg.Sort == "" {
		s.cfg.Sort = "hot"
	}
	if s.cfg.TimeFilter == "" {
		s.cfg.TimeFilter = "day"
	}
	if s.cfg.Limit <= 0 {
		s.cfg.Limit = defaultLimit
	}
	if s.cfg.Limit > maxLimit {
		s.cfg.Limit = maxLimit
	}
	return s
}

// Name implements ports.Source.
func (s *Source) Name() string { return s.name }

type listing struct {
	Data struct {
		Children []struct {
			Kind string `json:"kind"`
			Data post   `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

type post struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Permalink  string  `json:"permalink"`
	Author     string  `json:"author"`
	CreatedUTC float64 `json:"created_utc"`
	Score      int     `json:"score"`
	IsSelf     bool    `json:"is_self"`
	Selftext   string  `json:"selftext"`
}

// Fetch implements ports.Source.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]domain.Item, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	q := url.Values{"limit": {strconv.Itoa(s.cfg.Limit)}, "raw_json": {"1"}}
	if s.cfg.Sort == "top" {
		q.Set("t", s.cfg.TimeFilter)
	}
	endpoint := fmt.Sprintf("%s/r/%s/%s.json?%s", s.base, s.cfg.Subreddit, s.cfg.Sort, q.Encode())

	var l listing
	if err := s.get(ctx, endpoint, &l); err != nil {
		return nil, err
	}
	fetchedAt := time.Now().UTC()
	var items []domain.Item
	for _, c := range l.Data.Children {
		if c.Kind != "t3" {
			continue
		}
		p := c.Data
		published := time.Unix(int64(p.CreatedUTC), 0).UTC()
		if p.ID == "" || p.Title == "" || published.Before(since) || p.Score < s.cfg.MinScore {
			continue
		}
		discussion := DefaultBaseURL + p.Permalink
		u := p.URL
		if p.IsSelf || u == "" {
			u = discussion
		}
		items = append(items, domain.Item{
			SourceID:    s.sourceID,
			ExternalID:  p.ID,
			URL:         u,
			Title:       p.Title,
			Content:     textutil.Truncate(textutil.StripHTML(p.Selftext), maxContentRunes),
			Author:      p.Author,
			PublishedAt: published,
			FetchedAt:   fetchedAt,
		})
	}
	return items, nil
}

// get performs the request, honoring 429 Retry-After up to maxAttempts.
func (s *Source) get(ctx context.Context, endpoint string, v any) error {
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			wait := retryAfter(resp.Header.Get("Retry-After"), s.maxWait)
			resp.Body.Close()
			if attempt >= maxAttempts {
				return fmt.Errorf("%w after %d attempts", ErrRateLimited, attempt)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("reddit GET %s: status %d", endpoint, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return fmt.Errorf("reddit decode: %w", err)
		}
		return nil
	}
}

// retryAfter parses a Retry-After seconds header, capped at max.
func retryAfter(h string, max time.Duration) time.Duration {
	if h == "" {
		return min(defaultRetryAfter, max)
	}
	secs, err := strconv.Atoi(h)
	if err != nil || secs < 0 {
		return min(defaultRetryAfter, max)
	}
	return min(time.Duration(secs)*time.Second, max)
}
