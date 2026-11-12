// Package reddit is the Reddit source adapter. By default it reads the
// subreddit Atom feed (https://www.reddit.com/r/<sub>/<sort>/.rss), because
// Reddit answers 403 to unauthenticated JSON listing calls; the feed carries
// no score, so min_score is not applied there. With use_json the public
// listing JSON (/r/<sub>/<sort>.json) is tried first, falling back to the
// feed on 403.
//
// Rate limits are handled gently: on 429 the adapter honors Retry-After /
// x-ratelimit-reset with at most one retry, and when Reddit keeps limiting
// (or blocks the feed) the run is reported as skipped (domain.SkippedError)
// and the cooldown is remembered in-process so later runs inside it do not
// call Reddit at all.
package reddit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/jpdev/horizon-go/internal/adapters/source/internal/textutil"
	"github.com/jpdev/horizon-go/internal/domain"
)

// DefaultBaseURL is the public Reddit host.
const DefaultBaseURL = "https://www.reddit.com"

// UserAgent follows Reddit's "<platform>:<app id>:<version> (by ...)" format.
const UserAgent = "server:horizon-go:v0.1 (by JpDev; +https://github.com/Thysrael/Horizon)"

// ErrRateLimited is returned when Reddit keeps answering 429.
var ErrRateLimited = errors.New("reddit: rate limited (429)")

// ErrBlocked is returned when Reddit answers 403.
var ErrBlocked = errors.New("reddit: blocked (403)")

const (
	// maxAttempts is one request plus at most one retry after a 429.
	maxAttempts       = 2
	defaultRetryAfter = 5 * time.Second
	// defaultMaxWait caps the wait before the retry; it stays below the
	// collector's 30s per-source timeout.
	defaultMaxWait = 20 * time.Second
	// defaultCooldown applies when Reddit limits or blocks without saying
	// for how long.
	defaultCooldown = 10 * time.Minute
	defaultLimit    = 25
	maxLimit        = 100
	maxContentRunes = 1500
	maxBodyBytes    = 10 << 20
)

var (
	subredditRe = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)
	validSorts  = map[string]bool{"hot": true, "new": true, "top": true, "rising": true}
	linkHrefRe  = regexp.MustCompile(`<a href="([^"]+)">\[link\]</a>`)
)

// Config is the JSON stored in sources.config for kind=reddit.
type Config struct {
	Subreddit  string `json:"subreddit"`
	Sort       string `json:"sort,omitempty"`
	TimeFilter string `json:"time_filter,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	MinScore   int    `json:"min_score,omitempty"`
	// UseJSON tries the JSON listing (with scores) before the feed.
	UseJSON bool `json:"use_json,omitempty"`
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

// Fetch implements ports.Source. While Reddit's cooldown is active it returns
// a domain.SkippedError without any request.
func (s *Source) Fetch(ctx context.Context, since time.Time) ([]domain.Item, error) {
	if err := s.cfg.Validate(); err != nil {
		return nil, err
	}
	if left := cooldownLeft(s.base); left > 0 {
		return nil, skipped("reddit rate limited (cooldown)", left)
	}
	var (
		items []domain.Item
		err   error
	)
	if s.cfg.UseJSON {
		items, err = s.fetchJSON(ctx, since)
	} else {
		items, err = s.fetchRSS(ctx, since)
	}
	var lim *limitError
	if errors.As(err, &lim) {
		setCooldown(s.base, lim.retryAfter)
		return nil, skipped(lim.reason, lim.retryAfter)
	}
	if err == nil {
		clearCooldown(s.base)
	}
	return items, err
}

// fetchJSON reads the listing JSON, falling back to the feed on 403.
func (s *Source) fetchJSON(ctx context.Context, since time.Time) ([]domain.Item, error) {
	q := url.Values{"limit": {strconv.Itoa(s.cfg.Limit)}, "raw_json": {"1"}}
	if s.cfg.Sort == "top" {
		q.Set("t", s.cfg.TimeFilter)
	}
	endpoint := fmt.Sprintf("%s/r/%s/%s.json?%s", s.base, s.cfg.Subreddit, s.cfg.Sort, q.Encode())

	body, err := s.get(ctx, endpoint, "application/json")
	if errors.Is(err, ErrBlocked) {
		return s.fetchRSS(ctx, since)
	}
	if err != nil {
		return nil, err
	}
	var l listing
	if err := json.Unmarshal(body, &l); err != nil {
		return nil, fmt.Errorf("reddit decode: %w", err)
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

// fetchRSS reads the subreddit feed /r/<sub>/<sort>/.rss. A 403 here means
// there is nothing left to fall back to, so it becomes a skip.
func (s *Source) fetchRSS(ctx context.Context, since time.Time) ([]domain.Item, error) {
	endpoint := fmt.Sprintf("%s/r/%s/%s/.rss?limit=%d", s.base, s.cfg.Subreddit, s.cfg.Sort, s.cfg.Limit)
	body, err := s.get(ctx, endpoint, "application/atom+xml")
	if errors.Is(err, ErrBlocked) {
		return nil, &limitError{reason: "reddit blocked the feed (403)", retryAfter: defaultCooldown, err: err}
	}
	if err != nil {
		return nil, err
	}
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("reddit rss parse: %w", err)
	}
	fetchedAt := time.Now().UTC()
	var items []domain.Item
	for _, e := range feed.Items {
		published := e.PublishedParsed
		if published == nil {
			published = e.UpdatedParsed
		}
		if e.Title == "" || e.Link == "" || published == nil || published.Before(since) {
			continue
		}
		u := e.Link
		if m := linkHrefRe.FindStringSubmatch(e.Content); m != nil {
			u = m[1]
		}
		author := ""
		if len(e.Authors) > 0 && e.Authors[0] != nil {
			author = strings.TrimPrefix(e.Authors[0].Name, "/u/")
		}
		content := ""
		if u == e.Link {
			content = textutil.Truncate(textutil.StripHTML(e.Content), maxContentRunes)
		}
		items = append(items, domain.Item{
			SourceID:    s.sourceID,
			ExternalID:  strings.TrimPrefix(e.GUID, "t3_"),
			URL:         u,
			Title:       e.Title,
			Content:     content,
			Author:      author,
			PublishedAt: published.UTC(),
			FetchedAt:   fetchedAt,
		})
	}
	return items, nil
}

// limitError carries how long Reddit asked us to back off.
type limitError struct {
	reason     string
	retryAfter time.Duration
	err        error
}

func (e *limitError) Error() string { return e.reason }
func (e *limitError) Unwrap() error { return e.err }

// get performs the request and returns the body. On 429 it waits the time
// Reddit asks for (Retry-After or x-ratelimit-reset) and retries once, if
// that wait is within maxWait; otherwise it gives up at once with a
// *limitError. 403 maps to ErrBlocked.
func (s *Source) get(ctx context.Context, endpoint, accept string) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", accept)
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait, known := retryAfter(resp.Header, time.Now())
			if attempt >= maxAttempts || wait > s.maxWait {
				if !known {
					wait = defaultCooldown
				}
				return nil, &limitError{reason: "reddit rate limited (429)", retryAfter: wait,
					err: fmt.Errorf("%w after %d attempts", ErrRateLimited, attempt)}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		case resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("%w: GET %s: status 403", ErrBlocked, endpoint)
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("reddit GET %s: status %d", endpoint, resp.StatusCode)
		case readErr != nil:
			return nil, readErr
		}
		return body, nil
	}
}

// retryAfter reads how long Reddit wants us to wait from Retry-After
// (seconds or HTTP date) or x-ratelimit-reset (seconds). known is false when
// neither header is usable, in which case defaultRetryAfter is returned.
func retryAfter(h http.Header, now time.Time) (wait time.Duration, known bool) {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
			return secondsToDuration(secs), true
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(t.Sub(now), 0), true
		}
	}
	if v := strings.TrimSpace(h.Get("X-Ratelimit-Reset")); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
			return secondsToDuration(secs), true
		}
	}
	return defaultRetryAfter, false
}

func secondsToDuration(secs float64) time.Duration {
	return time.Duration(secs * float64(time.Second)).Round(time.Second)
}

// skipped builds the non-fatal outcome reported to the collector.
func skipped(reason string, retry time.Duration) *domain.SkippedError {
	secs := int(retry.Round(time.Second) / time.Second)
	return &domain.SkippedError{Reason: fmt.Sprintf("%s; retry after %ds", reason, secs), RetryAfter: retry}
}

// cooldowns remembers, per Reddit base URL, until when we must not call it.
// Reddit limits per client IP, so the whole host shares one cooldown. Sources
// are rebuilt every run, hence the package-level state.
var cooldowns = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

func cooldownLeft(base string) time.Duration {
	cooldowns.Lock()
	defer cooldowns.Unlock()
	return time.Until(cooldowns.until[base])
}

func setCooldown(base string, d time.Duration) {
	if d <= 0 {
		return
	}
	cooldowns.Lock()
	defer cooldowns.Unlock()
	cooldowns.until[base] = time.Now().Add(d)
}

func clearCooldown(base string) {
	cooldowns.Lock()
	defer cooldowns.Unlock()
	delete(cooldowns.until, base)
}
