package httpapi

// Integration test of every endpoint against the real Postgres+pgvector and
// NATS from CLAUDE.md, on a dedicated database (horizon_test_api) and a
// per-run JetStream stream. The feed_view rows are built by the real dedup
// projection (AIRepo.ApplyCluster). Source upstreams are an httptest server.
// When the infra is unreachable the test FAILS (no silent green).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	"github.com/jpdev/horizon-go/internal/adapters/mail/resend"
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/internal/digest"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ingest"
	"github.com/jpdev/horizon-go/internal/newsletter"
	"github.com/jpdev/horizon-go/internal/retention"
	"github.com/jpdev/horizon-go/migrations"
)

const testDBName = "horizon_test_api"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func testDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	adminDSN := config.DatabaseURL()
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
	mdb, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()
	if err := migrations.Run(mdb, "up"); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	pool, err := postgres.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE sources, items, fetch_runs, clusters, feed_view, ai_cache, digests, llm_usage, vote_rate_limits, newsletter_subscribers, newsletter_deliveries RESTART IDENTITY CASCADE;
UPDATE settings SET score_threshold = 6, digest_max_items = 20, digest_per_source = 8, digest_cron = '0 7 * * *', timezone = 'America/Sao_Paulo'`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	return pool
}

func testBus(t *testing.T, ctx context.Context) *natsbus.Bus {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := strings.ToUpper(hex.EncodeToString(b))
	stream := "ITEMS_API_" + id
	bus, err := natsbus.Connect(ctx, natsbus.Config{URL: env("NATS_URL", "nats://localhost:4230"), Stream: stream, SubjectPrefix: "api" + strings.ToLower(id) + ".items"})
	if err != nil {
		t.Fatalf("nats unreachable: %v", err)
	}
	t.Cleanup(func() {
		_ = bus.JetStream().DeleteStream(context.Background(), stream)
		bus.Close()
	})
	return bus
}

// vec is cos(a)*e_i + sin(a)*e_j in 768 dims.
func vec(i, j int, cos float64) []float32 {
	v := make([]float32, 768)
	v[i] = float32(cos)
	v[j] = float32(math.Sqrt(1 - cos*cos))
	return v
}

type env3 struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	srv  *httptest.Server
}

func (e env3) call(method, path, body string, want int, out any) string {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(e.ctx, method, e.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("%s %s: read: %v", method, path, err)
	}
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s: status %d want %d: %s", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode: %v: %s", method, path, err, b)
		}
	}
	return string(b)
}

func (e env3) scalar(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestAPIAgainstPostgresAndNATS(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	bus := testBus(t, ctx)
	now := time.Now().UTC()

	// Upstreams: the RSS feed answers, HN and Reddit are down.
	mux := http.NewServeMux()
	mux.HandleFunc("/feed.atom", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>T</title><id>t</id><updated>%[1]s</updated>
<entry><title>Fresh blog post one</title><id>b1</id><link rel="alternate" href="https://blog.example/one"/><published>%[1]s</published></entry>
<entry><title>Fresh blog post two</title><id>b2</id><link rel="alternate" href="https://blog.example/two"/><published>%[1]s</published></entry>
</feed>`, now.Add(-time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusInternalServerError) })
	up := httptest.NewServer(mux)
	defer up.Close()

	sources := postgres.SourceRepo{DB: pool}
	items := postgres.ItemRepo{DB: pool}
	ai := postgres.AIRepo{DB: pool}
	read := postgres.ReadRepo{DB: pool}
	base := source.BaseURLs{HackerNews: up.URL + "/hn", Reddit: up.URL + "/reddit"}

	// --- seed: 3 sources, 5 items, the real dedup projection ---
	hn, _ := sources.Create(ctx, domain.SourceConfig{Kind: domain.KindHackerNews, Name: "Hacker News", Enabled: true, Config: json.RawMessage(`{}`)})
	rd, _ := sources.Create(ctx, domain.SourceConfig{Kind: domain.KindReddit, Name: "r/golang", Enabled: true, Config: json.RawMessage(`{"subreddit":"golang"}`)})
	blog, err := sources.Create(ctx, domain.SourceConfig{Kind: domain.KindRSS, Name: "Blog", Enabled: true, Config: json.RawMessage(`{"url":"` + up.URL + `/feed.atom"}`)})
	if err != nil {
		t.Fatal(err)
	}
	type seed struct {
		src     int64
		title   string
		ago     time.Duration
		score   float64
		tags    []string
		emb     []float32
		matches []string
	}
	seeds := []struct {
		key string
		seed
	}{
		{"A", seed{hn.ID, "Go 1.27 released", 2 * time.Hour, 9, []string{"go", "release"}, vec(0, 1, 1), nil}},
		{"B", seed{rd.ID, "Go 1.27 is out", 90 * time.Minute, 7, []string{"go"}, vec(0, 1, 0.95), []string{"A"}}},
		{"C", seed{blog.ID, "Postgres 19 beta", time.Hour, 5, []string{"postgres"}, vec(3, 4, 1), nil}},
		{"D", seed{rd.ID, "Go survey results", 72 * time.Hour, 8, []string{"go", "survey"}, vec(5, 6, 1), nil}},
		{"E", seed{hn.ID, "Unscored story", 30 * time.Minute, 0, nil, nil, nil}},
	}
	ids := map[string]int64{}
	for _, s := range seeds {
		id, err := items.Upsert(ctx, domain.Item{SourceID: s.src, ExternalID: s.key, URL: "https://news.example/" + s.key,
			Title: s.title, PublishedAt: now.Add(-s.ago), FetchedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		ids[s.key] = id
		if s.emb == nil {
			continue
		}
		if _, err := ai.SaveScore(ctx, id, domain.Analysis{Score: s.score, Reason: "why " + s.key, Summary: "summary " + s.key, Tags: s.tags}, s.emb); err != nil {
			t.Fatal(err)
		}
		var matched []int64
		for _, m := range s.matches {
			matched = append(matched, ids[m])
		}
		if _, err := ai.ApplyCluster(ctx, id, matched); err != nil {
			t.Fatal(err)
		}
	}

	usage := postgres.UsageRepo{DB: pool}
	for _, u := range []domain.TokenUsage{
		{Kind: "chat", Model: "qwen3:14b", PromptTokens: 800, CompletionTokens: 120},
		{Kind: "chat", Model: "qwen3:14b", PromptTokens: 700, CompletionTokens: 100},
		{Kind: "embed", Model: "nomic-embed-text", PromptTokens: 40},
	} {
		if err := usage.Record(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	persister, err := bus.NewPersister(ctx, "api-persister", items, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipe := &ingest.Pipeline{
		Sources:   sources,
		Collector: &app.Collector{Publisher: bus, Runs: postgres.FetchRunRepo{DB: pool}, Timeout: 5 * time.Second},
		Client:    up.Client(),
		BaseURLs:  base,
	}
	s := &Server{
		Store: read, Sources: sources, Digests: &digest.Service{Store: read}, Collector: pipe,
		ValidateSource: func(c domain.SourceConfig) error { _, err := source.Build(c, nil, base); return err },
		ValidateCron:   digest.ValidateCron,
		Checks:         []Check{{Name: "postgres", Fn: read.Ping}, {Name: "nats", Fn: bus.Ping}},
		AllowedOrigins: []string{"http://localhost:5173"},
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	e := env3{t: t, ctx: ctx, pool: pool, srv: srv}

	// --- GET /healthz ---
	var health map[string]string
	e.call("GET", "/healthz", "", 200, &health)
	if health["postgres"] != "ok" || health["nats"] != "ok" {
		t.Fatalf("healthz: %v", health)
	}

	// --- GET /api/feed (feed_view only: A primary of {A,B}, C, D; E unscored) ---
	feedIDs := func(path string, wantTotal int) []int64 {
		t.Helper()
		var p domain.FeedPage
		e.call("GET", path, "", 200, &p)
		if p.Total != wantTotal {
			t.Fatalf("%s: total %d want %d", path, p.Total, wantTotal)
		}
		out := make([]int64, len(p.Items))
		for i, it := range p.Items {
			out[i] = it.ItemID
		}
		return out
	}
	expect := func(path string, wantTotal int, keys ...string) {
		t.Helper()
		got := feedIDs(path, wantTotal)
		want := make([]int64, len(keys))
		for i, k := range keys {
			want[i] = ids[k]
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: got %v want %v (%v)", path, got, want, keys)
		}
	}
	expect("/api/feed", 3, "A", "D", "C")
	expect("/api/feed?source=rss", 1, "C")
	expect(fmt.Sprintf("/api/feed?source_id=%d", rd.ID), 1, "D")
	expect("/api/feed?tag=GO", 2, "A", "D")
	expect("/api/feed?min_score=6", 2, "A", "D")
	expect("/api/feed?q=postgres", 1, "C")
	expect("/api/feed?q=SUMMARY%20d", 1, "D")
	expect("/api/feed?period=24h", 2, "A", "C")
	expect("/api/feed?sort=recent", 3, "C", "A", "D")
	expect("/api/feed?page=2&page_size=1", 3, "D")
	expect("/api/feed?q=%25", 0)
	// --- GET /api/feed/timeline (date scrubber histogram) ---
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	dayOf := func(k string) string {
		for _, s := range seeds {
			if s.key == k {
				return now.Add(-s.ago).In(loc).Format(time.DateOnly)
			}
		}
		t.Fatalf("no seed %s", k)
		return ""
	}
	timeline := func(path string) map[string]int {
		t.Helper()
		var tl domain.FeedTimeline
		e.call("GET", path, "", 200, &tl)
		got := map[string]int{}
		sum := 0
		for i, b := range tl.Buckets {
			if b.Count > 0 {
				got[b.Day] = b.Count
			}
			sum += b.Count
			if i > 0 && !b.Start.After(tl.Buckets[i-1].Start) {
				t.Fatalf("%s: buckets out of order: %+v", path, tl.Buckets)
			}
		}
		if len(tl.Buckets) != tl.Days || sum != tl.Total || tl.Timezone != "America/Sao_Paulo" ||
			!tl.Buckets[0].Start.Equal(tl.From) || !tl.To.After(now) {
			t.Fatalf("%s: shape: %+v", path, tl)
		}
		return got
	}
	wantDays := func(keys ...string) map[string]int {
		m := map[string]int{}
		for _, k := range keys {
			m[dayOf(k)]++
		}
		return m
	}
	for path, want := range map[string]map[string]int{
		"/api/feed/timeline?days=7":                   wantDays("A", "C", "D"),
		"/api/feed/timeline?days=7&min_score=6":       wantDays("A", "D"),
		"/api/feed/timeline?days=7&tag=go&period=24h": wantDays("A", "D"), // time filters are ignored
		"/api/feed/timeline?days=7&q=postgres":        wantDays("C"),
	} {
		if got := timeline(path); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: got %v want %v", path, got, want)
		}
	}
	// The range picked on the scrubber, sent back as from/to, returns the
	// same rows the histogram counted.
	var tl domain.FeedTimeline
	e.call("GET", "/api/feed/timeline?days=2", "", 200, &tl)
	var ranged domain.FeedPage
	e.call("GET", "/api/feed?from="+url.QueryEscape(tl.From.Format(time.RFC3339))+"&to="+url.QueryEscape(tl.To.Format(time.RFC3339)), "", 200, &ranged)
	if ranged.Total != tl.Total {
		t.Fatalf("feed in timeline range: %d rows, histogram says %d", ranged.Total, tl.Total)
	}

	var first domain.FeedPage
	e.call("GET", "/api/feed?page_size=1", "", 200, &first)
	if it := first.Items[0]; it.DuplicateCount != 1 || it.ScoreReason != "why A" || it.SourceName != "Hacker News" || it.ClusterID == nil {
		t.Fatalf("feed row: %+v", it)
	}

	// --- GET /api/items/{id} ---
	var b domain.ItemDetail
	e.call("GET", fmt.Sprintf("/api/items/%d", ids["B"]), "", 200, &b)
	if b.Cluster == nil || b.Cluster.ItemCount != 2 || b.IsPrimary || b.Status != "duplicate" || b.ScoreReason != "why B" ||
		len(b.Duplicates) != 1 || b.Duplicates[0].ID != ids["A"] || b.Duplicates[0].Similarity == nil || math.Abs(*b.Duplicates[0].Similarity-0.95) > 1e-3 {
		t.Fatalf("item B: %+v dups=%+v", b, b.Duplicates)
	}
	var a domain.ItemDetail
	e.call("GET", fmt.Sprintf("/api/items/%d", ids["A"]), "", 200, &a)
	if !a.IsPrimary || len(a.Duplicates) != 1 || a.Duplicates[0].ID != ids["B"] || *a.Score != 9 {
		t.Fatalf("item A: %+v", a)
	}
	var raw domain.ItemDetail
	e.call("GET", fmt.Sprintf("/api/items/%d", ids["E"]), "", 200, &raw)
	if raw.Cluster != nil || raw.Score != nil || len(raw.Duplicates) != 0 || raw.Status != "raw" {
		t.Fatalf("item E: %+v", raw)
	}
	e.call("GET", "/api/items/999999", "", 404, nil)

	// --- GET/PUT /api/settings ---
	var st domain.Settings
	e.call("GET", "/api/settings", "", 200, &st)
	if st.ScoreThreshold != 6 || st.DigestMaxItems != 20 || st.DigestPerSource != 8 || st.DigestCron != "0 7 * * *" || st.Timezone != "America/Sao_Paulo" {
		t.Fatalf("default settings: %+v", st)
	}
	e.call("PUT", "/api/settings", `{"score_threshold":5,"digest_max_items":10,"digest_per_source":1,"digest_cron":"30 6 * * *"}`, 200, &st)
	if n := e.scalar(`SELECT count(*) FROM settings WHERE score_threshold = 5 AND digest_max_items = 10 AND digest_per_source = 1 AND digest_cron = '30 6 * * *'`); n != 1 {
		t.Fatalf("settings not persisted")
	}
	e.call("PUT", "/api/settings", `{"score_threshold":5,"digest_max_items":10,"digest_per_source":1,"digest_cron":"61 6 * * *"}`, 400, nil)

	// --- POST /api/digests/generate: last 24h = A (9), C (5); B is not in
	// feed_view, D is 3 days old. Threshold 5 and one per source keep A and C.
	var d domain.Digest
	e.call("POST", "/api/digests/generate", "", 201, &d)
	if fmt.Sprint(d.ItemIDs) != fmt.Sprint([]int64{ids["A"], ids["C"]}) || d.Considered != 2 || d.Language != "pt-BR" ||
		!strings.Contains(d.Markdown, "Go 1.27 released") || !strings.Contains(d.Markdown, "+1 fonte com a mesma notícia") ||
		!strings.Contains(d.HTML, "<article") {
		t.Fatalf("digest: %+v", d)
	}
	e.call("PUT", "/api/settings", `{"score_threshold":6,"digest_max_items":10,"digest_per_source":1,"digest_cron":"30 6 * * *"}`, 200, nil)
	e.call("POST", "/api/digests/generate", `{"date":"`+d.Date+`"}`, 201, &d)
	if len(d.ItemIDs) != 1 || d.ItemIDs[0] != ids["A"] {
		t.Fatalf("regenerated digest: %v", d.ItemIDs)
	}
	if n := e.scalar(`SELECT count(*) FROM digests WHERE digest_date = $1::date AND cardinality(item_ids) = 1 AND markdown LIKE '%Go 1.27 released%' AND html <> ''`, d.Date); n != 1 {
		t.Fatalf("digest row not upserted: %d", n)
	}
	e.call("POST", "/api/digests/generate", `{"date":"2999-01-01"}`, 400, nil)

	// --- GET /api/digests, /api/digests/{date} ---
	var list struct{ Digests []domain.Digest }
	e.call("GET", "/api/digests", "", 200, &list)
	if len(list.Digests) != 1 || list.Digests[0].Date != d.Date || list.Digests[0].Markdown != "" || list.Digests[0].ItemCount != 1 {
		t.Fatalf("digest list: %+v", list.Digests)
	}
	var one domain.Digest
	e.call("GET", "/api/digests/"+d.Date, "", 200, &one)
	if one.Markdown != d.Markdown || one.HTML == "" {
		t.Fatal("digest by date differs")
	}
	if md := e.call("GET", "/api/digests/"+d.Date+"?format=md", "", 200, nil); !strings.HasPrefix(md, "# Radar by JpDev") {
		t.Fatalf("markdown body: %.80s", md)
	}
	e.call("GET", "/api/digests/2001-01-01", "", 404, nil)

	// --- /api/sources CRUD ---
	var srcs struct{ Sources []sourceDTO }
	e.call("GET", "/api/sources", "", 200, &srcs)
	if len(srcs.Sources) != 3 || *srcs.Sources[0].ItemCount != 2 || srcs.Sources[2].Kind != "rss" {
		t.Fatalf("sources: %+v", srcs.Sources)
	}
	e.call("POST", "/api/sources", `{"kind":"rss","name":"Bad","config":{"url":"not a url"}}`, 400, nil)
	var created sourceDTO
	e.call("POST", "/api/sources", `{"kind":"reddit","name":"r/rust","config":{"subreddit":"rust"},"enabled":false}`, 201, &created)
	if n := e.scalar(`SELECT count(*) FROM sources WHERE id = $1 AND kind = 'reddit' AND NOT enabled AND config->>'subreddit' = 'rust'`, created.ID); n != 1 {
		t.Fatal("created source not persisted")
	}
	e.call("POST", "/api/sources", `{"kind":"reddit","name":"r/rust","config":{"subreddit":"rust"}}`, 409, nil)
	var patched sourceDTO
	e.call("PATCH", fmt.Sprintf("/api/sources/%d", created.ID), `{"enabled":true,"name":"r/rust (hot)"}`, 200, &patched)
	if !patched.Enabled || patched.Name != "r/rust (hot)" || e.scalar(`SELECT count(*) FROM sources WHERE id = $1 AND enabled AND name = 'r/rust (hot)'`, created.ID) != 1 {
		t.Fatalf("patch: %+v", patched)
	}
	e.call("PATCH", fmt.Sprintf("/api/sources/%d", created.ID), `{"config":{"subreddit":""}}`, 400, nil)
	e.call("DELETE", fmt.Sprintf("/api/sources/%d", created.ID), "", 204, nil)
	if e.scalar(`SELECT count(*) FROM sources WHERE id = $1`, created.ID) != 0 {
		t.Fatal("source not deleted")
	}
	e.call("DELETE", fmt.Sprintf("/api/sources/%d", created.ID), "", 404, nil)

	// --- POST /api/sources/{id}/fetch: real collection -> NATS -> persister -> items ---
	var rep reportDTO
	e.call("POST", fmt.Sprintf("/api/sources/%d/fetch", blog.ID), "", 200, &rep)
	if len(rep.Sources) != 1 || rep.Sources[0].Status != "ok" || rep.Sources[0].ItemCount != 2 || rep.Published != 2 {
		t.Fatalf("fetch report: %+v", rep)
	}
	if n, err := persister.Drain(ctx); err != nil || n != 2 {
		t.Fatalf("persist: %d %v", n, err)
	}
	if n := e.scalar(`SELECT count(*) FROM items WHERE source_id = $1 AND title LIKE 'Fresh blog post%'`, blog.ID); n != 2 {
		t.Fatalf("fetched items in db: %d", n)
	}
	if n := e.scalar(`SELECT count(*) FROM fetch_runs WHERE run_id = $1 AND status = 'ok' AND item_count = 2`, rep.RunID); n != 1 {
		t.Fatal("fetch run not recorded")
	}
	e.call("POST", "/api/sources/999999/fetch", "", 404, nil)

	// --- POST /api/pipeline/run: every enabled source; HN and Reddit fail ---
	e.call("POST", "/api/pipeline/run", "", 200, &rep)
	status := map[string]string{}
	for _, f := range rep.Sources {
		status[f.SourceName] = f.Status
	}
	if len(rep.Sources) != 3 || status["Blog"] != "ok" || status["Hacker News"] != "error" || status["r/golang"] != "error" {
		t.Fatalf("pipeline report: %+v", rep)
	}
	if n := e.scalar(`SELECT count(*) FROM fetch_runs WHERE run_id = $1`, rep.RunID); n != 3 {
		t.Fatalf("pipeline fetch runs: %d", n)
	}
	if _, err := persister.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	// --- GET /api/metrics ---
	var m domain.Metrics
	e.call("GET", "/api/metrics?days=7", "", 200, &m)
	today := m.ItemsPerDay[len(m.ItemsPerDay)-1]
	if m.Days != 7 || len(m.ItemsPerDay) != 7 || today.Fetched != 7 || today.Scored != 4 {
		t.Fatalf("items per day: %+v", m.ItemsPerDay)
	}
	if m.Dedup.Clustered != 4 || m.Dedup.Duplicates != 1 || m.Dedup.Rate != 0.25 || m.Dedup.Clusters != 3 || m.Dedup.MultiItemClusters != 1 {
		t.Fatalf("dedup: %+v", m.Dedup)
	}
	if m.Failures.FetchErrors != 2 || len(m.Failures.Recent) != 2 || m.Status["raw"] != 3 {
		t.Fatalf("failures/status: %+v %v", m.Failures, m.Status)
	}
	if m.Tokens.Calls != 3 || m.Tokens.PromptTokens != 1540 || m.Tokens.CompletionTokens != 220 || m.Tokens.TotalTokens != 1760 || len(m.Tokens.PerDay) != 2 {
		t.Fatalf("tokens: %+v", m.Tokens)
	}
	var blogM domain.SourceMetrics
	for _, sm := range m.BySource {
		if sm.SourceID == blog.ID {
			blogM = sm
		}
	}
	if blogM.Items != 3 || blogM.FeedItems != 1 || blogM.FetchOK != 2 || blogM.LastStatus != "ok" || blogM.AvgScore == nil || *blogM.AvgScore != 5 {
		t.Fatalf("blog metrics: %+v", blogM)
	}

	// items and feed_items use the same clock (fetch time): D was published
	// 3 days ago but fetched today, so it counts in a 1-day window.
	e.call("GET", "/api/metrics?days=1", "", 200, &m)
	for _, sm := range m.BySource {
		if sm.SourceID == rd.ID && (sm.Items != 2 || sm.FeedItems != 1) {
			t.Fatalf("reddit 1-day metrics: %+v", sm)
		}
	}

	// --- CORS on the real router ---
	req, _ := http.NewRequest("GET", srv.URL+"/api/feed", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("cors: %v", resp.Header)
	}
}

func TestVotesAndTrendingIntegration(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	now := time.Now().UTC()

	sources := postgres.SourceRepo{DB: pool}
	items := postgres.ItemRepo{DB: pool}
	ai := postgres.AIRepo{DB: pool}
	read := postgres.ReadRepo{DB: pool}
	votes := postgres.VoteRepo{DB: pool}
	clean := postgres.CleanupRepo{DB: pool}

	hn, err := sources.Create(ctx, domain.SourceConfig{Kind: domain.KindHackerNews, Name: "Hacker News", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	type seed struct {
		key   string
		ago   time.Duration
		score float64
	}
	ids := map[string]int64{}
	for _, s := range []seed{
		{"hi", 2 * time.Hour, 9.0},
		{"mid", 2 * time.Hour, 7.0},
		{"old", 90 * time.Hour, 9.5},
	} {
		id, err := items.Upsert(ctx, domain.Item{
			SourceID: hn.ID, ExternalID: s.key, URL: "https://news.example/" + s.key,
			Title: "Story " + s.key, PublishedAt: now.Add(-s.ago), FetchedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ai.SaveScore(ctx, id, domain.Analysis{Score: s.score, Reason: "why", Summary: "sum", Tags: []string{"ai"}}, vec(1, 2, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := ai.ApplyCluster(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		ids[s.key] = id
	}

	s := &Server{
		Store:          read,
		Sources:        sources,
		Votes:          votes,
		VoteHashSecret: []byte("0123456789abcdef0123456789abcdef"),
		VoteMinDwell:   3 * time.Second,
		VoteRateWindow: time.Minute,
		VoteRateMax:    5,
		VoteRateTTL:    time.Hour,
		RequireAdmin:   true,
		AdminToken:     "admin-secret",
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	e := env3{t: t, ctx: ctx, pool: pool, srv: srv}

	doDev := func(method, path, body, dev string, want int, out any) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if dev != "" {
			req.Header.Set("X-Device-Id", dev)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status %d want %d (%s)", method, path, resp.StatusCode, want, string(raw))
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("%s %s: unmarshal: %v (%s)", method, path, err, string(raw))
			}
		}
	}

	// Dwell check before 3s fails (400).
	doDev("POST", fmt.Sprintf("/api/items/%d/vote", ids["mid"]), `{"vote":1,"dwell_ms":1200}`, "device_one_12345678", 400, nil)

	// Device 1 likes "mid", Device 2 likes then swaps to dislike (dislikes stay hidden).
	var vr domain.VoteResult
	doDev("POST", fmt.Sprintf("/api/items/%d/vote", ids["mid"]), `{"vote":1,"dwell_ms":3500}`, "device_one_12345678", 200, &vr)
	if vr.Likes != 1 || vr.UserVote != 1 {
		t.Fatalf("vr1: %+v", vr)
	}
	doDev("POST", fmt.Sprintf("/api/items/%d/vote", ids["mid"]), `{"vote":1,"dwell_ms":3500}`, "device_two_12345678", 200, &vr)
	if vr.Likes != 2 || vr.UserVote != 1 {
		t.Fatalf("vr2: %+v", vr)
	}
	// Changing vote on same device does not double count likes, switching to -1 decrements likes and hides dislike count.
	doDev("POST", fmt.Sprintf("/api/items/%d/vote", ids["mid"]), `{"vote":-1,"dwell_ms":3500}`, "device_two_12345678", 200, &vr)
	if vr.Likes != 1 || vr.UserVote != -1 {
		t.Fatalf("vr2 swap: %+v", vr)
	}

	// GET /api/items/{id} includes Likes and caller's UserVote.
	var detail domain.ItemDetail
	doDev("GET", fmt.Sprintf("/api/items/%d", ids["mid"]), "", "device_two_12345678", 200, &detail)
	if detail.Likes != 1 || detail.UserVote != -1 {
		t.Fatalf("item detail votes: %+v", detail)
	}

	// sort=score: AI score remains primary despite votes ("old" 9.5 > "hi" 9.0 > "mid" 7.0).
	var page domain.FeedPage
	doDev("GET", "/api/feed?sort=score", "", "device_one_12345678", 200, &page)
	if len(page.Items) != 3 || page.Items[0].ItemID != ids["old"] || page.Items[1].ItemID != ids["hi"] || page.Items[2].ItemID != ids["mid"] {
		t.Fatalf("score order violated AI base: %+v", page.Items)
	}
	if page.Items[2].Likes != 1 || page.Items[2].UserVote != 1 {
		t.Fatalf("feed item vote fields: %+v", page.Items[2])
	}

	// sort=trending: excludes "old" (>72h), ranks remaining recent items cleanly.
	doDev("GET", "/api/feed?sort=trending", "", "device_one_12345678", 200, &page)
	if len(page.Items) != 2 || page.Items[0].ItemID == ids["old"] || page.Items[1].ItemID == ids["old"] {
		t.Fatalf("trending should exclude >72h item: %+v", page.Items)
	}

	// Rate limit kicks in on 6th vote (VoteRateMax = 5; we already sent 3 valid votes, send 2 more then 1 blocked).
	doDev("POST", fmt.Sprintf("/api/items/%d/vote", ids["hi"]), `{"vote":1,"dwell_ms":3500}`, "device_one_12345678", 200, nil)
	doDev("POST", fmt.Sprintf("/api/items/%d/vote", ids["hi"]), `{"vote":0,"dwell_ms":0}`, "device_one_12345678", 200, nil)
	doDev("POST", fmt.Sprintf("/api/items/%d/vote", ids["hi"]), `{"vote":1,"dwell_ms":3500}`, "device_one_12345678", 429, nil)

	// Expired vote_rate_limits rows are cleaned by Retention Cleanup.
	if _, err := pool.Exec(ctx, `UPDATE vote_rate_limits SET expires_at = $1`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := clean.Cleanup(ctx, retention.Params{Cutoff: now.Add(-32 * 24 * time.Hour), CutoffDate: now.Add(-32 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if n := e.scalar(`SELECT count(*) FROM vote_rate_limits`); n != 0 {
		t.Fatalf("expected expired rate limits pruned, got %d", n)
	}
}

func TestNewsletterPostgresIntegration(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	now := time.Now().UTC()

	sources := postgres.SourceRepo{DB: pool}
	items := postgres.ItemRepo{DB: pool}
	ai := postgres.AIRepo{DB: pool}
	src, err := sources.Create(ctx, domain.SourceConfig{Kind: domain.KindHackerNews, Name: "HN", Enabled: true, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		id, err := items.Upsert(ctx, domain.Item{
			SourceID: src.ID, ExternalID: fmt.Sprintf("nl-%d", i),
			URL: fmt.Sprintf("https://news.example/nl-%d", i), Title: fmt.Sprintf("Newsletter Story %d", i),
			PublishedAt: now.Add(-time.Duration(i) * time.Hour), FetchedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ai.SaveScore(ctx, id, domain.Analysis{Score: 8.5 - float64(i)*0.2, Reason: "great", Summary: "summary", Tags: []string{"go"}}, vec(2, i, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := ai.ApplyCluster(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
	}

	type mailReq struct {
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
		Text    string   `json:"text"`
	}
	var mails []mailReq
	resendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var mr mailReq
		_ = json.NewDecoder(r.Body).Decode(&mr)
		mails = append(mails, mr)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"re_test_1"}`))
	}))
	defer resendSrv.Close()

	nlSvc := &newsletter.Service{
		Store:    postgres.NewsletterRepo{DB: pool},
		Mailer:   &resend.Client{APIKey: "test-key", From: "Horizon <test@example.com>", BaseURL: resendSrv.URL},
		FrontURL: "https://horizon.example",
	}
	s := &Server{
		Store:        postgres.ReadRepo{DB: pool},
		Sources:      sources,
		Newsletter:   nlSvc,
		RequireAdmin: true,
		AdminToken:   "nl-admin-secret",
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	reqJSON := func(method, path, body, token string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s -> %d want %d (%s)", method, path, resp.StatusCode, want, string(raw))
		}
		return string(raw)
	}

	// Double opt-in subscribe.
	reqJSON("POST", "/api/newsletter/subscribe", `{"email":"Reader@Example.com"}`, "", 200)
	if len(mails) != 1 || mails[0].To[0] != "reader@example.com" {
		t.Fatalf("expected 1 confirmation mail, got %+v", mails)
	}
	confirmIdx := strings.Index(mails[0].Text, "/newsletter/confirm?token=")
	if confirmIdx < 0 {
		t.Fatalf("missing confirm URL in email: %s", mails[0].Text)
	}
	confirmTok := strings.Fields(mails[0].Text[confirmIdx+len("/newsletter/confirm?token="):])[0]

	// Confirm activates subscriber.
	reqJSON("POST", "/api/newsletter/confirm", fmt.Sprintf(`{"token":%q}`, confirmTok), "", 200)

	// Protected send fails without admin token (401).
	reqJSON("POST", "/api/newsletter/send", `{"dry_run":false}`, "", 401)

	// Protected admin send delivers Top 7 edition with unsubscribe link.
	out := reqJSON("POST", "/api/newsletter/send", `{"dry_run":false}`, "nl-admin-secret", 200)
	var rep newsletter.SendReport
	_ = json.Unmarshal([]byte(out), &rep)
	if rep.ItemCount != 7 || rep.SentCount != 1 || len(mails) != 2 {
		t.Fatalf("expected Top 7 and 1 sent email, rep=%+v mails=%d", rep, len(mails))
	}
	unsubIdx := strings.Index(mails[1].Text, "/newsletter/unsubscribe?token=")
	if unsubIdx < 0 {
		t.Fatalf("missing unsubscribe link in daily edition: %s", mails[1].Text)
	}
	unsubTok := strings.Fields(mails[1].Text[unsubIdx+len("/newsletter/unsubscribe?token="):])[0]

	// Unsubscribe deactivates subscriber so next force dry-run reports 0 recipients.
	reqJSON("POST", "/api/newsletter/unsubscribe", fmt.Sprintf(`{"token":%q}`, unsubTok), "", 200)
	out2 := reqJSON("POST", "/api/newsletter/send", `{"dry_run":true}`, "nl-admin-secret", 200)
	var rep2 newsletter.SendReport
	_ = json.Unmarshal([]byte(out2), &rep2)
	if rep2.RecipientCount != 0 {
		t.Fatalf("expected 0 recipients after unsubscribe, got %+v", rep2)
	}
}
