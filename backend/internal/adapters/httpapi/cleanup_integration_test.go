package httpapi

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/retention"
)

func TestCleanupRetentionAgainstPostgres(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	if _, err := pool.Exec(ctx, `TRUNCATE alert_events RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}

	loc, err := time.LoadLocation(retention.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, loc)
	cutoff := now.AddDate(0, 0, -retention.RetentionDays)
	old := cutoff.Add(-24 * time.Hour)
	fresh := cutoff.Add(24 * time.Hour)

	var sourceID int64
	if err := pool.QueryRow(ctx, `INSERT INTO sources (kind, name, config) VALUES ('push', 'cleanup test', '{}') RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	insertItem := func(external string, published any, fetched time.Time) int64 {
		t.Helper()
		var id int64
		err := pool.QueryRow(ctx, `
INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, published_at, fetched_at)
VALUES ($1, $2, $3, $3, encode(sha256(convert_to($3, 'UTF8')), 'hex'), $2, $4, $5)
RETURNING id`, sourceID, external, "https://cleanup.example/"+external, published, fetched).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldPublished := insertItem("old-published", old, now)
	oldFetched := insertItem("old-fetched", nil, old)
	freshItem := insertItem("fresh", fresh, now)

	var oldCluster, mixedCluster int64
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (title) VALUES ('old cluster') RETURNING id`).Scan(&oldCluster); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO clusters (title) VALUES ('mixed cluster') RETURNING id`).Scan(&mixedCluster); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE items SET cluster_id = CASE WHEN id = $1 THEN $4::bigint WHEN id = $2 THEN $4::bigint WHEN id = $3 THEN $5::bigint END WHERE id IN ($1, $2, $3)`, oldPublished, oldFetched, freshItem, oldCluster, mixedCluster); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE clusters SET representative_item_id = CASE id WHEN $1 THEN $2 WHEN $2 THEN $3 END, item_count = CASE id WHEN $1 THEN 1 WHEN $2 THEN 2 END WHERE id IN ($1, $2)`, oldCluster, oldPublished, freshItem); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO feed_view (item_id, source_id, source_kind, source_name, title, url, score, published_at) VALUES ($1, $2, 'push', 'cleanup test', 'old', 'https://cleanup.example/old-published', 5, $3), ($2, $2, 'push', 'cleanup test', 'fresh', 'https://cleanup.example/fresh', 6, $4)`, oldPublished, freshItem, old, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO digests (digest_date, language, title, markdown, item_ids) VALUES ($1::date, 'pt-BR', 'old', '# old', ARRAY[$2]::bigint[])`, old, oldPublished); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO digests (digest_date, language, title, markdown, item_ids) VALUES ($1::date, 'pt-BR', 'fresh', '# fresh', ARRAY[$2]::bigint[])`, fresh, freshItem); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO fetch_runs (run_id, source_id, status, started_at, finished_at) VALUES ('old', $1, 'ok', $2, $2)`, sourceID, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO fetch_runs (run_id, source_id, status, started_at, finished_at) VALUES ('fresh', $1, 'ok', $2, $2)`, sourceID, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO alert_events (kind, name, message, at) VALUES ('test', 'old', 'old', $1)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO alert_events (kind, name, message, at) VALUES ('test', 'fresh', 'fresh', $1)`, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_cache (kind, key, model, value, created_at) VALUES ('score', 'old', 'test', '{}', $1)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_cache (kind, key, model, value, created_at) VALUES ('score', 'fresh', 'test', '{}', $1)`, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO llm_usage (day, kind, model, calls) VALUES ($1::date, 'chat', 'test', 1)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO llm_usage (day, kind, model, calls) VALUES ($1::date, 'chat', 'test', 1)`, fresh); err != nil {
		t.Fatal(err)
	}

	repo := postgres.CleanupRepo{DB: pool}
	params := retention.Params{Cutoff: cutoff, CutoffDate: cutoff, DryRun: true}
	dry, err := repo.Cleanup(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"items": 2, "feed_view": 1, "clusters": 1, "digests": 1, "fetch_runs": 1, "alert_events": 1, "ai_cache": 1, "llm_usage": 1} {
		if dry[key] != want {
			t.Errorf("dry %s=%d want %d (counts=%v)", key, dry[key], want, dry)
		}
	}
	if got := scalarCleanup(t, ctx, pool, `SELECT count(*) FROM items`); got != 3 {
		t.Fatalf("dry-run changed items: %d", got)
	}

	params.DryRun = false
	real, err := repo.Cleanup(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if real["items"] != 2 || real["feed_view"] != 1 || real["clusters"] != 1 {
		t.Fatalf("real counts=%v", real)
	}
	for name, want := range map[string]int{"items": 1, "feed_view": 1, "clusters": 1, "digests": 1, "fetch_runs": 1, "alert_events": 1, "ai_cache": 1, "llm_usage": 1, "sources": 1, "settings": 1} {
		var q string
		switch name {
		case "items":
			q = `SELECT count(*) FROM items`
		case "feed_view":
			q = `SELECT count(*) FROM feed_view`
		case "clusters":
			q = `SELECT count(*) FROM clusters`
		case "digests":
			q = `SELECT count(*) FROM digests`
		case "fetch_runs":
			q = `SELECT count(*) FROM fetch_runs`
		case "alert_events":
			q = `SELECT count(*) FROM alert_events`
		case "ai_cache":
			q = `SELECT count(*) FROM ai_cache`
		case "llm_usage":
			q = `SELECT count(*) FROM llm_usage`
		case "sources":
			q = `SELECT count(*) FROM sources`
		case "settings":
			q = `SELECT count(*) FROM settings`
		}
		if got := scalarCleanup(t, ctx, pool, q); got != want {
			t.Errorf("%s=%d want %d", name, got, want)
		}
	}
	if got := scalarCleanup(t, ctx, pool, `SELECT count(*) FROM items WHERE published_at IS NULL AND fetched_at < $1`, cutoff); got != 0 {
		t.Fatal("old NULL published item survived")
	}
	if got := scalarCleanup(t, ctx, pool, `SELECT count(*) FROM items WHERE id = $1 AND cluster_id = $2`, freshItem, mixedCluster); got != 1 {
		t.Fatal("fresh item lost its cluster")
	}
	if got := scalarCleanup(t, ctx, pool, `SELECT count(*) FROM clusters WHERE id = $1`, oldCluster); got != 0 {
		t.Fatal("empty old cluster survived")
	}
}

func scalarCleanup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}
