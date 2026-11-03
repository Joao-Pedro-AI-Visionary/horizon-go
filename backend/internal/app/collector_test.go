package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

type fakeSource struct {
	name  string
	items []domain.Item
	err   error
	delay time.Duration
	panic bool
}

func (f fakeSource) Name() string { return f.name }
func (f fakeSource) Fetch(ctx context.Context, _ time.Time) ([]domain.Item, error) {
	if f.panic {
		panic("boom")
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.items, f.err
}

type fakePublisher struct {
	mu    sync.Mutex
	runID string
	items []domain.Item
}

func (p *fakePublisher) PublishRaw(_ context.Context, runID string, items []domain.Item) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runID = runID
	p.items = append(p.items, items...)
	return nil
}

type fakeRuns struct {
	mu   sync.Mutex
	runs []domain.FetchRun
}

func (r *fakeRuns) Insert(_ context.Context, f domain.FetchRun) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, f)
	return int64(len(r.runs)), nil
}

func TestCollectorFanOutDedupAndReport(t *testing.T) {
	pub, runs := &fakePublisher{}, &fakeRuns{}
	c := Collector{Publisher: pub, Runs: runs, Timeout: 200 * time.Millisecond}
	jobs := []Job{
		{SourceID: 1, Source: fakeSource{name: "hn", items: []domain.Item{
			{SourceID: 1, ExternalID: "1", URL: "https://www.example.com/post/?utm_source=hn", Title: "A", Content: "short"},
			{SourceID: 1, ExternalID: "2", URL: "https://example.com/other", Title: "B"},
			{SourceID: 1, ExternalID: "3", URL: "notaurl", Title: "bad"},
		}}},
		{SourceID: 2, Source: fakeSource{name: "reddit", items: []domain.Item{
			{SourceID: 2, ExternalID: "x", URL: "https://example.com/post#comments", Title: "A again", Content: "much longer content"},
		}}},
		{SourceID: 3, Source: fakeSource{name: "slow", delay: 5 * time.Second}},
		{SourceID: 4, Source: fakeSource{name: "broken", err: errors.New("upstream 500")}},
		{SourceID: 5, Source: fakeSource{name: "panics", panic: true}},
	}

	start := time.Now()
	rep, err := c.Run(context.Background(), jobs, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("per-source timeout not applied, took %v", elapsed)
	}

	if rep.Fetched != 4 || rep.Invalid != 1 || rep.Duplicates != 1 || rep.Published != 2 {
		t.Fatalf("report counts: %+v", rep)
	}
	if len(pub.items) != 2 || pub.runID != rep.RunID || rep.RunID == "" {
		t.Fatalf("published %d items run=%q report=%q", len(pub.items), pub.runID, rep.RunID)
	}
	var merged domain.Item
	for _, it := range pub.items {
		if it.URLHash == "" || it.URLNormalized == "" {
			t.Fatalf("item not normalized before publish: %+v", it)
		}
		if it.URLNormalized == "https://example.com/post" {
			merged = it
		}
	}
	if merged.Content != "much longer content" {
		t.Fatalf("dedup must keep the richest content, got %+v", merged)
	}

	if len(runs.runs) != 5 {
		t.Fatalf("want one fetch_run per source, got %d", len(runs.runs))
	}
	byID := map[int64]domain.FetchRun{}
	for _, r := range runs.runs {
		if r.RunID != rep.RunID || r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
			t.Fatalf("bad run row: %+v", r)
		}
		byID[r.SourceID] = r
	}
	if r := byID[1]; r.Status != domain.FetchOK || r.ItemCount != 3 {
		t.Fatalf("hn run: %+v", r)
	}
	if r := byID[2]; r.Status != domain.FetchOK || r.ItemCount != 1 {
		t.Fatalf("reddit run: %+v", r)
	}
	for _, id := range []int64{3, 4, 5} {
		if r := byID[id]; r.Status != domain.FetchError || r.Error == "" {
			t.Fatalf("source %d must be reported as error: %+v", id, r)
		}
	}
	if byID[3].Error != context.DeadlineExceeded.Error() {
		t.Fatalf("slow source error = %q", byID[3].Error)
	}
}

func TestCollectorNoJobs(t *testing.T) {
	pub := &fakePublisher{}
	rep, err := (&Collector{Publisher: pub, Runs: &fakeRuns{}}).Run(context.Background(), nil, time.Now())
	if err != nil || rep.Published != 0 || len(pub.items) != 0 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}
