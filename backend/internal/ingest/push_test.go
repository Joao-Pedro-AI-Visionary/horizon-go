package ingest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

type fakeEnsurer struct{ got domain.SourceConfig }

func (f *fakeEnsurer) Ensure(_ context.Context, s domain.SourceConfig) (domain.SourceConfig, error) {
	f.got = s
	s.ID = 42
	return s, nil
}

type fakeHashes map[string]bool

func (f fakeHashes) ExistingURLHashes(_ context.Context, hs []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, h := range hs {
		if f[h] {
			out[h] = true
		}
	}
	return out, nil
}

type fakePub struct {
	items []domain.Item
	err   error
}

func (f *fakePub) PublishRaw(_ context.Context, _ string, items []domain.Item) error {
	f.items = append(f.items, items...)
	return f.err
}

func hashOf(t *testing.T, u string) string {
	it := domain.Item{URL: u}
	if err := it.Prepare(); err != nil {
		t.Fatal(err)
	}
	return it.URLHash
}

func TestPushPublishesDedupedItems(t *testing.T) {
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	pub := &fakePub{}
	ens := &fakeEnsurer{}
	stored := fakeHashes{hashOf(t, "https://x.com/a/status/3"): true}
	p := &Pusher{Sources: ens, Items: stored, Publisher: pub, Now: func() time.Time { return now }}
	at := now.Add(-2 * time.Hour)
	rep, err := p.Push(context.Background(), []PushItem{
		{Source: "X", URL: "https://x.com/a/status/1", Title: "New local model", Text: "Runs on a laptop", Author: "@a", PublishedAt: &at,
			Metrics: &PushMetrics{Views: 1_250_000, Likes: 4300, Reposts: 512}},
		{Source: "X", URL: "https://x.com/a/status/1?utm_source=x", Text: "same url again"},
		{Source: "X", URL: "https://x.com/a/status/2", Text: "Title from text\nsecond line"},
		{Source: "X", URL: "https://x.com/a/status/3", Text: "already stored"},
		{Source: "X", URL: "ftp://bad", Text: "bad url"},
		{Source: "X", URL: "https://x.com/a/status/4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ens.got.Kind != domain.KindPush || ens.got.Name != PushSourceName {
		t.Fatalf("ensured %+v", ens.got)
	}
	if rep.Received != 6 || rep.Accepted != 2 || rep.Duplicates != 2 || rep.Invalid != 2 || rep.SourceID != 42 {
		t.Fatalf("report %+v", rep)
	}
	if len(pub.items) != 2 {
		t.Fatalf("published %d", len(pub.items))
	}
	a, b := pub.items[0], pub.items[1]
	if a.SourceID != 42 || a.Title != "New local model" || !a.PublishedAt.Equal(at) || a.Author != "@a" || a.ExternalID != a.URLHash {
		t.Errorf("item a %+v", a)
	}
	if !strings.Contains(a.Content, "Engagement: 1.2M views, 4.3K likes, 512 reposts") {
		t.Errorf("content %q", a.Content)
	}
	if b.Title != "Title from text" || !b.PublishedAt.Equal(now) {
		t.Errorf("item b %+v", b)
	}
}

func TestPushRejectsBadBatches(t *testing.T) {
	p := &Pusher{Sources: &fakeEnsurer{}, Items: fakeHashes{}, Publisher: &fakePub{}}
	if _, err := p.Push(context.Background(), nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty: %v", err)
	}
	big := make([]PushItem, MaxPushBatch+1)
	if _, err := p.Push(context.Background(), big); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("too big: %v", err)
	}
}

func TestPushReportsPublishErrors(t *testing.T) {
	p := &Pusher{Sources: &fakeEnsurer{}, Items: fakeHashes{}, Publisher: &fakePub{err: errors.New("nats down")}}
	if _, err := p.Push(context.Background(), []PushItem{{URL: "https://x.com/1", Title: "t"}}); err == nil {
		t.Fatal("want error")
	}
}
