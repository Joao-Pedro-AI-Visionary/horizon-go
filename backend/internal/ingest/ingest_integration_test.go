package ingest

// Integration test against the real Postgres and NATS from CLAUDE.md.
// It uses a dedicated database (horizon_test) and a per-run JetStream stream,
// so dev data is untouched. Set HORIZON_SKIP_INTEGRATION=1 to skip; when the
// infra is unreachable the test FAILS (no silent green).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/migrations"
)

const (
	defaultAdminDSN = "postgres://horizon:horizon@localhost:5450/horizon?sslmode=disable"
	defaultNATS     = "nats://localhost:4230"
	testDBName      = "horizon_test"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// testDB creates horizon_test if needed, migrates it and empties it.
func testDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	adminDSN := env("DATABASE_URL", defaultAdminDSN)
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var exists bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, testDBName).Scan(&exists); err != nil {
		t.Fatalf("postgres unreachable at %s: %v", adminDSN, err)
	}
	if !exists {
		if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+testDBName); err != nil {
			t.Fatalf("create test db: %v", err)
		}
	}

	u, _ := url.Parse(adminDSN)
	u.Path = "/" + testDBName
	dsn := env("TEST_DATABASE_URL", u.String())
	mdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()
	if err := migrations.Run(mdb, "up"); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	pool, err := postgres.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE sources, items, fetch_runs, clusters, feed_view RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func testBus(t *testing.T, ctx context.Context) *natsbus.Bus {
	t.Helper()
	id := strings.ToUpper(randHex(4))
	bus, err := natsbus.Connect(ctx, natsbus.Config{
		URL:           env("NATS_URL", defaultNATS),
		Stream:        "ITEMS_IT_" + id,
		SubjectPrefix: "it" + strings.ToLower(id) + ".items",
	})
	if err != nil {
		t.Fatalf("nats unreachable: %v", err)
	}
	t.Cleanup(func() {
		_ = bus.JetStream().DeleteStream(context.Background(), "ITEMS_IT_"+id)
		bus.Close()
	})
	return bus
}

// fakeUpstreams serves HN, Reddit and two feeds (one healthy, one failing).
func fakeUpstreams(t *testing.T, now time.Time) *httptest.Server {
	t.Helper()
	recent := now.Add(-time.Hour)
	mux := http.NewServeMux()
	mux.HandleFunc("/hn/topstories.json", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[101,102,103]`)
	})
	mux.HandleFunc("/hn/item/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hn/item/101.json":
			fmt.Fprintf(w, `{"id":101,"type":"story","by":"a","title":"Shared story","url":"https://www.shared.example/post/?utm_source=hn","score":300,"time":%d}`, recent.Unix())
		case "/hn/item/102.json":
			fmt.Fprintf(w, `{"id":102,"type":"story","by":"b","title":"HN only","url":"https://hn-only.example/x","score":150,"time":%d}`, recent.Unix())
		case "/hn/item/103.json":
			fmt.Fprintf(w, `{"id":103,"type":"story","by":"c","title":"Low","url":"https://low.example/x","score":3,"time":%d}`, recent.Unix())
		}
	})
	mux.HandleFunc("/reddit/r/golang/hot.json", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "horizon-go") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"data":{"children":[
		 {"kind":"t3","data":{"id":"r1","title":"Reddit link","url":"https://reddit-link.example/a","permalink":"/r/golang/comments/r1/x/","author":"g","created_utc":%d,"score":50,"is_self":false}},
		 {"kind":"t3","data":{"id":"r2","title":"Reddit self","url":"","permalink":"/r/golang/comments/r2/y/","author":"h","created_utc":%d,"score":20,"is_self":true,"selftext":"question body"}}
		]}}`, recent.Unix(), recent.Unix())
	})
	mux.HandleFunc("/feed.atom", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>T</title><id>t</id><updated>%[1]s</updated>
<entry><title>Shared story (blog)</title><id>blog-1</id><link rel="alternate" href="https://shared.example/post#top"/><published>%[1]s</published><summary>The longest and richest description of the shared story.</summary></entry>
<entry><title>Blog only</title><id>blog-2</id><link rel="alternate" href="https://blog.example/only/"/><published>%[1]s</published></entry>
</feed>`, recent.Format(time.RFC3339))
	})
	mux.HandleFunc("/broken.atom", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestIngestionEndToEnd(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	now := time.Now().UTC()

	pool := testDB(t, ctx)
	bus := testBus(t, ctx)
	up := fakeUpstreams(t, now)
	sources := postgres.SourceRepo{DB: pool}
	items := postgres.ItemRepo{DB: pool}
	runs := postgres.FetchRunRepo{DB: pool}

	// --- sources CRUD through the repo ---
	mk := func(kind, name, cfg string, enabled bool) domain.SourceConfig {
		s, err := sources.Create(ctx, domain.SourceConfig{Kind: kind, Name: name, Config: json.RawMessage(cfg), Enabled: enabled})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return s
	}
	hnSrc := mk(domain.KindHackerNews, "hn", `{"min_score":100}`, true)
	rdSrc := mk(domain.KindReddit, "r/golang", `{"subreddit":"golang","sort":"hot"}`, true)
	feed := mk(domain.KindRSS, "blog", `{"url":"`+up.URL+`/feed.atom"}`, true)
	broken := mk(domain.KindRSS, "broken", `{"url":"`+up.URL+`/broken.atom"}`, true)
	disabled := mk(domain.KindRSS, "disabled", `{"url":"`+up.URL+`/feed.atom"}`, false)
	temp := mk(domain.KindRSS, "temp", `{"url":"https://temp.example/feed"}`, true)

	if _, err := sources.Create(ctx, domain.SourceConfig{Kind: domain.KindHackerNews, Name: "hn"}); !errors.Is(err, postgres.ErrConflict) {
		t.Fatalf("duplicate kind+name must conflict, got %v", err)
	}
	got, err := sources.Get(ctx, rdSrc.ID)
	if err != nil || got.Name != "r/golang" || !got.Enabled {
		t.Fatalf("get: %+v %v", got, err)
	}
	disabled.Name = "disabled-renamed"
	if upd, err := sources.Update(ctx, disabled); err != nil || upd.Name != "disabled-renamed" || upd.Enabled {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if err := sources.Delete(ctx, temp.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := sources.Delete(ctx, temp.ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("second delete must be not found, got %v", err)
	}
	enabled, err := sources.List(ctx, true)
	if err != nil || len(enabled) != 4 {
		t.Fatalf("list enabled: %d %v", len(enabled), err)
	}

	// --- pipeline: collector -> NATS -> durable consumer -> items ---
	persister, err := bus.NewPersister(ctx, "it-persister", items, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{
		Sources:   sources,
		Collector: &app.Collector{Publisher: bus, Runs: runs, Timeout: 5 * time.Second},
		Persister: persister,
		Client:    up.Client(),
		BaseURLs:  source.BaseURLs{HackerNews: up.URL + "/hn", Reddit: up.URL + "/reddit"},
	}
	res, err := p.Once(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	// HN 2 + Reddit 2 + blog 2 = 6 fetched; shared URL appears twice -> 5 unique.
	if res.Report.Fetched != 6 || res.Report.Duplicates != 1 || res.Report.Published != 5 || res.Persisted != 5 {
		t.Fatalf("report: %+v persisted=%d", res.Report, res.Persisted)
	}

	rows, err := pool.Query(ctx, `SELECT url_normalized, source_id, content FROM items ORDER BY url_normalized COLLATE "C"`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		url     string
		source  int64
		content string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.url, &r.source, &r.content); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	rows.Close()
	wantURLs := []string{
		"https://blog.example/only",
		"https://hn-only.example/x",
		"https://reddit-link.example/a",
		"https://reddit.com/r/golang/comments/r2/y",
		"https://shared.example/post",
	}
	if len(all) != len(wantURLs) {
		t.Fatalf("items rows = %d, want %d: %+v", len(all), len(wantURLs), all)
	}
	for i, w := range wantURLs {
		if all[i].url != w {
			t.Fatalf("row %d url = %q, want %q", i, all[i].url, w)
		}
	}
	shared := all[4]
	if shared.source != feed.ID || !strings.Contains(shared.content, "richest") {
		t.Fatalf("cross-source duplicate must keep the richest item: %+v", shared)
	}

	report, err := runs.ListByRun(ctx, res.Report.RunID)
	if err != nil || len(report) != 4 {
		t.Fatalf("fetch_runs for run: %d %v", len(report), err)
	}
	want := map[int64]struct {
		status string
		count  int
	}{hnSrc.ID: {"ok", 2}, rdSrc.ID: {"ok", 2}, feed.ID: {"ok", 2}, broken.ID: {"error", 0}}
	for _, r := range report {
		w := want[r.SourceID]
		if r.Status != w.status || r.ItemCount != w.count {
			t.Fatalf("fetch_run %s: %+v want %+v", r.SourceName, r, w)
		}
		if r.Status == "error" && !strings.Contains(r.Error, "500") {
			t.Fatalf("error message not recorded: %+v", r)
		}
	}

	// --- second run is idempotent on items (upsert by url_hash) ---
	res2, err := p.Once(ctx, now.Add(-24*time.Hour))
	if err != nil || res2.Persisted != 5 {
		t.Fatalf("second Once: %+v %v", res2, err)
	}
	var n, nRuns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("items after rerun = %d (%v), want 5", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fetch_runs`).Scan(&nRuns); err != nil || nRuns != 8 {
		t.Fatalf("fetch_runs after rerun = %d (%v), want 8", nRuns, err)
	}
}
