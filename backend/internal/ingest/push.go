package ingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// PushSourceName is the kind=push source that receives the X (Twitter) radar.
const PushSourceName = "X (radar)"

// MaxPushBatch bounds one POST /api/items/ingest call.
const MaxPushBatch = 50

const (
	maxPushTitleRunes   = 200
	maxPushContentRunes = 3000
)

// PushMetrics is the engagement reported by the pusher (optional).
type PushMetrics struct {
	Views   int64 `json:"views"`
	Likes   int64 `json:"likes"`
	Reposts int64 `json:"reposts"`
}

// PushItem is one entry of POST /api/items/ingest.
type PushItem struct {
	Source      string       `json:"source"`
	URL         string       `json:"url"`
	Title       string       `json:"title"`
	Text        string       `json:"text"`
	Author      string       `json:"author"`
	PublishedAt *time.Time   `json:"published_at"`
	Metrics     *PushMetrics `json:"metrics"`
}

// PushReport is the outcome of one push.
type PushReport struct {
	RunID      string   `json:"run_id"`
	SourceID   int64    `json:"source_id"`
	SourceName string   `json:"source_name"`
	Received   int      `json:"received"`
	Accepted   int      `json:"accepted"`
	Duplicates int      `json:"duplicates"` // same URL in the batch or already stored
	Invalid    int      `json:"invalid"`
	Errors     []string `json:"errors,omitempty"`
}

// SourceEnsurer creates a source row if missing and returns it.
type SourceEnsurer interface {
	Ensure(ctx context.Context, s domain.SourceConfig) (domain.SourceConfig, error)
}

// URLHashChecker reports which URL hashes are already stored.
type URLHashChecker interface {
	ExistingURLHashes(ctx context.Context, hashes []string) (map[string]bool, error)
}

// Pusher turns pushed entries into raw items on the bus, so they follow the
// normal persist -> score -> embed -> dedup path.
type Pusher struct {
	Sources   SourceEnsurer
	Items     URLHashChecker
	Publisher ports.ItemPublisher
	Now       func() time.Time
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// firstLine is the text up to the first line break, for a missing title.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

func compact(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// content is the text plus a metrics line the scorer can weigh.
func (p PushItem) content() string {
	text := strings.TrimSpace(p.Text)
	var meta []string
	if s := strings.TrimSpace(p.Source); s != "" {
		meta = append(meta, "Source: "+s)
	}
	if m := p.Metrics; m != nil && (m.Views > 0 || m.Likes > 0 || m.Reposts > 0) {
		meta = append(meta, fmt.Sprintf("Engagement: %s views, %s likes, %s reposts", compact(m.Views), compact(m.Likes), compact(m.Reposts)))
	}
	if len(meta) > 0 {
		text = strings.TrimSpace(text + "\n\n" + strings.Join(meta, ". ") + ".")
	}
	return truncate(text, maxPushContentRunes)
}

func newPushRunID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "push-" + hex.EncodeToString(b)
}

// Push validates and publishes the entries. Entries with a bad URL or
// neither title nor text are counted as invalid; the rest of the batch goes
// on. Duplicates (same normalized URL in the batch or already stored) are
// skipped.
func (ps *Pusher) Push(ctx context.Context, in []PushItem) (PushReport, error) {
	rep := PushReport{Received: len(in), RunID: newPushRunID()}
	if len(in) == 0 {
		return rep, domain.Invalidf("items: send a non-empty JSON array")
	}
	if len(in) > MaxPushBatch {
		return rep, domain.Invalidf("items: at most %d per request", MaxPushBatch)
	}
	src, err := ps.Sources.Ensure(ctx, domain.SourceConfig{Kind: domain.KindPush, Name: PushSourceName, Enabled: true, Config: []byte(`{}`)})
	if err != nil {
		return rep, fmt.Errorf("ensure push source: %w", err)
	}
	rep.SourceID, rep.SourceName = src.ID, src.Name
	now := time.Now
	if ps.Now != nil {
		now = ps.Now
	}
	fetched := now().UTC()

	var items []domain.Item
	seen := map[string]bool{}
	for i, p := range in {
		title := truncate(p.Title, maxPushTitleRunes)
		if title == "" {
			title = truncate(firstLine(p.Text), maxPushTitleRunes)
		}
		u := strings.TrimSpace(p.URL)
		if title == "" || !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) {
			rep.Invalid++
			rep.Errors = append(rep.Errors, fmt.Sprintf("item %d: url (http/https) and title or text are required", i))
			continue
		}
		published := fetched
		if p.PublishedAt != nil && !p.PublishedAt.IsZero() && !p.PublishedAt.After(fetched.Add(time.Hour)) {
			published = p.PublishedAt.UTC()
		}
		it := domain.Item{
			SourceID:    src.ID,
			URL:         u,
			Title:       title,
			Content:     p.content(),
			Author:      truncate(p.Author, 100),
			PublishedAt: published,
			FetchedAt:   fetched,
		}
		if err := it.Prepare(); err != nil {
			rep.Invalid++
			rep.Errors = append(rep.Errors, fmt.Sprintf("item %d: %v", i, err))
			continue
		}
		if seen[it.URLHash] {
			rep.Duplicates++
			continue
		}
		seen[it.URLHash] = true
		it.ExternalID = it.URLHash
		items = append(items, it)
	}
	if len(items) == 0 {
		return rep, nil
	}
	hashes := make([]string, len(items))
	for i, it := range items {
		hashes[i] = it.URLHash
	}
	existing, err := ps.Items.ExistingURLHashes(ctx, hashes)
	if err != nil {
		return rep, fmt.Errorf("check stored urls: %w", err)
	}
	fresh := items[:0]
	for _, it := range items {
		if existing[it.URLHash] {
			rep.Duplicates++
			continue
		}
		fresh = append(fresh, it)
	}
	if len(fresh) == 0 {
		return rep, nil
	}
	if err := ps.Publisher.PublishRaw(ctx, rep.RunID, fresh); err != nil {
		return rep, fmt.Errorf("publish: %w", err)
	}
	rep.Accepted = len(fresh)
	return rep, nil
}
