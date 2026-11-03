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
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/digest"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ingest"
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
	adminDSN := env("DATABASE_URL", "postgres://horizon:horizon@localhost:5450/horizon?sslmode=disable")
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
	if _, err := pool.Exec(ctx, `TRUNCATE sources, items, fetch_runs, clusters, feed_view, ai_cache, digests, llm_usage RESTART IDENTITY CASCADE;
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
