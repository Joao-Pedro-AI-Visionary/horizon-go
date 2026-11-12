package ingest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
)

// Pushed items reach the items table through NATS like collected ones, the
// push source is created once and never fetched, and stored URLs are skipped.
func TestPushEndToEnd(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := testDB(t, ctx)
	bus := testBus(t, ctx)
	sources := postgres.SourceRepo{DB: pool}
	items := postgres.ItemRepo{DB: pool}

	persister, err := bus.NewPersister(ctx, "it-push-persister", items, nil)
	if err != nil {
		t.Fatal(err)
	}
	ps := &Pusher{Sources: sources, Items: items, Publisher: bus}
	batch := []PushItem{
		{Source: "X", URL: "https://x.com/dev/status/1", Title: "Open model drop", Text: "weights on HF", Author: "@dev",
			Metrics: &PushMetrics{Views: 90000, Likes: 1200, Reposts: 300}},
		{Source: "X", URL: "https://x.com/dev/status/2", Text: "Agent harness tip"},
	}
	rep, err := ps.Push(ctx, batch)
	if err != nil || rep.Accepted != 2 {
		t.Fatalf("push: %+v %v", rep, err)
	}
	if n, err := persister.Drain(ctx); err != nil || n != 2 {
		t.Fatalf("drain: %d %v", n, err)
	}
	var kind, status string
	var count int
	if err := pool.QueryRow(ctx, `SELECT s.kind, min(i.status), count(*) FROM items i JOIN sources s ON s.id = i.source_id
WHERE s.name = $1 GROUP BY s.kind`, PushSourceName).Scan(&kind, &status, &count); err != nil {
		t.Fatal(err)
	}
	if kind != domain.KindPush || status != "raw" || count != 2 {
		t.Fatalf("stored kind=%s status=%s count=%d", kind, status, count)
	}

	again, err := ps.Push(ctx, batch[:1])
	if err != nil || again.Accepted != 0 || again.Duplicates != 1 || again.SourceID != rep.SourceID {
		t.Fatalf("second push: %+v %v", again, err)
	}

	p := &Pipeline{Sources: sources, Collector: &app.Collector{Publisher: bus, Runs: postgres.FetchRunRepo{DB: pool}}}
	jobs, err := p.Jobs(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("push sources must not become collector jobs: %d %v", len(jobs), err)
	}
	if _, err := p.CollectSource(ctx, rep.SourceID, time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("fetching a push source must fail")
	}
}
