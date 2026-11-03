package enrich

// Integration test against the real Postgres+pgvector and NATS from
// CLAUDE.md, on a dedicated database (horizon_test_enrich, so it does not race
// the ingest package test) and a per-run JetStream stream. The model ports are
// fakes: vectors are built so that cosine similarities are known exactly.
// When the infra is unreachable the test FAILS (no silent green).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
	"github.com/jpdev/horizon-go/migrations"
)

const testDBName = "horizon_test_enrich"

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
	if _, err := pool.Exec(ctx, `TRUNCATE sources, items, fetch_runs, clusters, feed_view, ai_cache RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func testBus(t *testing.T, ctx context.Context) *natsbus.Bus {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := strings.ToUpper(hex.EncodeToString(b))
	stream := "ITEMS_EN_" + id
	bus, err := natsbus.Connect(ctx, natsbus.Config{
		URL:           env("NATS_URL", "nats://localhost:4230"),
		Stream:        stream,
		SubjectPrefix: "en" + strings.ToLower(id) + ".items",
	})
	if err != nil {
		t.Fatalf("nats unreachable: %v", err)
	}
	t.Cleanup(func() {
		_ = bus.JetStream().DeleteStream(context.Background(), stream)
		bus.Close()
	})
	return bus
}

// unit returns cos(a)*e_i + sin(a)*e_j in 768 dims; the cosine between
// unit(i, j, a) and e_i is cos(a).
func unit(i, j int, cos float64) []float32 {
	v := make([]float32, 768)
	v[i] = float32(cos)
	v[j] = float32(math.Sqrt(1 - cos*cos))
	return v
}

func neg(v []float32) []float32 {
	out := append([]float32{}, v...)
	for i := range out {
		if i != 0 {
			out[i] = -out[i]
		}
	}
	return out
}

// fixture: title -> score and vector. A is the reference (e0).
var fixture = []struct {
	key   string
	title string
	score float64
	ago   time.Duration
	vec   []float32
}{
	{"F", "Old Go 1.27 release event-x", 6, 5 * 24 * time.Hour, unit(0, 1, 1)},  // same vector as A, outside 72h
	{"A", "Go 1.27 released event-x", 6, 3 * time.Hour, unit(0, 1, 1)},          // reference
	{"B", "Go 1.27 is out event-x", 9, 2 * time.Hour, unit(0, 1, 0.95)},         // A·B = .95 -> direct duplicate
	{"C", "Go 1.27 release notes event-x", 7, time.Hour, neg(unit(0, 1, 0.84))}, // A·C = .84 ambiguous, LLM: same
	{"D", "Go developer survey results", 5, 30 * time.Minute, unit(0, 2, 0.84)}, // A·D = .84 ambiguous, LLM: different; B·D = .798
	{"E", "Postgres 19 beta", 8, 10 * time.Minute, unit(3, 4, 1)},               // orthogonal
}

func fixtureByTitle(s string) (int, bool) {
	for i, f := range fixture {
		if strings.Contains(s, f.title) {
			return i, true
		}
	}
	return 0, false
}

// fakeModel plays both ports (test-only).
type fakeModel struct {
	mu         sync.Mutex
	scoreCalls int
	dedupCalls int
	embedCalls int
}

var titleLine = regexp.MustCompile(`(?m)^\[(\d+)\] (.*)$`)

func (f *fakeModel) Model() string { return "fake" }

func (f *fakeModel) Chat(_ context.Context, req ports.ChatRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(req.System, "deduplication assistant") {
		f.dedupCalls++
		// Same event = every listed item whose title carries "event-x".
		var idx []string
		for _, m := range titleLine.FindAllStringSubmatch(req.User, -1) {
			if strings.Contains(m[2], "event-x") {
				idx = append(idx, m[1])
			}
		}
		return fmt.Sprintf(`{"duplicates": [[%s]]}`, strings.Join(idx, ",")), nil
	}
	f.scoreCalls++
	i, ok := fixtureByTitle(req.User)
	if !ok {
		return "", fmt.Errorf("unknown item in prompt")
	}
	fx := fixture[i]
	return fmt.Sprintf(`{"score": %v, "reason": "r %s", "summary": "s %s", "tags": ["go", "Test"]}`, fx.score, fx.key, fx.key), nil
}

type fakeEmbed struct{ m *fakeModel }

func (e fakeEmbed) Model() string { return "fake-embed" }
func (e fakeEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.m.mu.Lock()
	e.m.embedCalls++
	e.m.mu.Unlock()
	out := make([][]float32, len(texts))
	for k, s := range texts {
		i, ok := fixtureByTitle(s)
		if !ok {
			return nil, fmt.Errorf("unknown text %q", s)
		}
		out[k] = fixture[i].vec
	}
	return out, nil
}

func TestScoreAndDedupAgainstPgvector(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	bus := testBus(t, ctx)

	src, err := postgres.SourceRepo{DB: pool}.Create(ctx, domain.SourceConfig{Kind: domain.KindRSS, Name: "test feed", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	repo := postgres.ItemRepo{DB: pool}
	var raw []domain.Item
	for _, f := range fixture {
		it := domain.Item{SourceID: src.ID, ExternalID: f.key, URL: "https://example.test/" + f.key, Title: f.title,
			Content: "content " + f.key, PublishedAt: now.Add(-f.ago), FetchedAt: now}
		if err := it.Prepare(); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Upsert(ctx, it); err != nil {
			t.Fatal(err)
		}
		raw = append(raw, it)
	}
	// Half of the items arrive through items.raw, the rest only via backfill.
	if err := bus.PublishRaw(ctx, "run-1", raw[:3]); err != nil {
		t.Fatal(err)
	}

	model := &fakeModel{}
	an := &app.Analyzer{LLM: model, Embedder: fakeEmbed{model}, Cache: postgres.AICache{DB: pool}, Retry: retry.Policy{Attempts: 2, Base: time.Millisecond}}
	ai := postgres.AIRepo{DB: pool}

	rawCons, err := bus.Consumer(ctx, "it-scorer", bus.RawSubject(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	scorer := &Scorer{Repo: ai, Analyzer: an, Bus: bus, Workers: 3}
	if err := scorer.Drain(ctx, rawCons); err != nil {
		t.Fatal(err)
	}
	if got := scorer.Stats.Scored.Load(); got != int64(len(fixture)) {
		t.Fatalf("scored = %d, want %d (failed %d)", got, len(fixture), scorer.Stats.Failed.Load())
	}

	var scored, withEmb int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'scored' AND score IS NOT NULL), count(embedding) FROM items`).Scan(&scored, &withEmb); err != nil {
		t.Fatal(err)
	}
	if scored != len(fixture) || withEmb != len(fixture) {
		t.Fatalf("scored rows = %d, embeddings = %d", scored, withEmb)
	}
	var tags []string
	var reason string
	if err := pool.QueryRow(ctx, `SELECT tags, score_reason FROM items WHERE external_id = 'B'`).Scan(&tags, &reason); err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "go,test" || reason != "r B" {
		t.Errorf("B tags=%v reason=%q", tags, reason)
	}

	scoredCons, err := bus.Consumer(ctx, "it-deduper", bus.ScoredSubject(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	info, err := scoredCons.Info(ctx)
	if err != nil || info.NumPending != uint64(len(fixture)) {
		t.Fatalf("items.scored pending = %v (%v), want %d", info.NumPending, err, len(fixture))
	}
	ded := &Deduper{Repo: ai, Analyzer: an, Bus: bus}
	if err := ded.Drain(ctx, scoredCons); err != nil {
		t.Fatal(err)
	}
	if ded.Stats.Processed != len(fixture) || ded.Stats.Failed != 0 {
		t.Fatalf("dedup stats = %+v", ded.Stats)
	}
	// LLM asked only for C (A in band) and D (A in band).
	if model.dedupCalls != 2 {
		t.Errorf("dedup LLM calls = %d, want 2", model.dedupCalls)
	}

	// Clusters: {F}, {A,B,C} with B primary, {D}, {E}.
	rows, err := pool.Query(ctx, `
SELECT c.item_count, p.external_id, array_agg(i.external_id ORDER BY i.external_id)::text
FROM clusters c JOIN items p ON p.id = c.representative_item_id JOIN items i ON i.cluster_id = c.id
GROUP BY c.id, c.item_count, p.external_id ORDER BY 3`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var n int
		var primary, members string
		if err := rows.Scan(&n, &primary, &members); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%s:%d", members, primary, n))
	}
	want := []string{"{A,B,C}:B:3", "{D}:D:1", "{E}:E:1", "{F}:F:1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("clusters = %v, want %v", got, want)
	}

	var dupStatus string
	if err := pool.QueryRow(ctx, `SELECT string_agg(external_id || '=' || status, ',' ORDER BY external_id) FROM items`).Scan(&dupStatus); err != nil {
		t.Fatal(err)
	}
	if dupStatus != "A=duplicate,B=deduped,C=duplicate,D=deduped,E=deduped,F=deduped" {
		t.Errorf("statuses = %s", dupStatus)
	}

	// Projection: one row per cluster (the primary), with the duplicate count.
	var feed string
	if err := pool.QueryRow(ctx, `
SELECT string_agg(i.external_id || ':' || f.duplicate_count || ':' || f.score, ',' ORDER BY f.score DESC, i.external_id)
FROM feed_view f JOIN items i ON i.id = f.item_id`).Scan(&feed); err != nil {
		t.Fatal(err)
	}
	if feed != "B:2:9,E:0:8,F:0:6,D:0:5" {
		t.Errorf("feed_view = %s", feed)
	}

	// Centroid of {A,B,C} is the average embedding.
	var centroidSim float64
	if err := pool.QueryRow(ctx, `
SELECT 1 - (c.centroid <=> (SELECT embedding FROM items WHERE external_id = 'A'))
FROM clusters c JOIN items p ON p.id = c.representative_item_id WHERE p.external_id = 'B'`).Scan(&centroidSim); err != nil {
		t.Fatal(err)
	}
	if centroidSim < 0.9 {
		t.Errorf("centroid similarity to A = %v", centroidSim)
	}

	// items.deduped carries one event per item.
	dedCons, err := bus.Consumer(ctx, "it-deduped", bus.DedupedSubject(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := dedCons.Info(ctx); err != nil || info.NumPending != uint64(len(fixture)) {
		t.Errorf("items.deduped pending = %v (%v)", info.NumPending, err)
	}

	// Cache by content hash: a reset item is re-scored without model calls.
	before := model.scoreCalls
	if _, err := pool.Exec(ctx, `UPDATE items SET score = NULL, status = 'raw', cluster_id = NULL WHERE external_id = 'E'`); err != nil {
		t.Fatal(err)
	}
	again := &Scorer{Repo: ai, Analyzer: an, Bus: bus, Workers: 1}
	if err := again.Drain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if again.Stats.Scored.Load() != 1 || model.scoreCalls != before {
		t.Errorf("rescored=%d model calls %d -> %d (want cache hit)", again.Stats.Scored.Load(), before, model.scoreCalls)
	}
	var cache string
	if err := pool.QueryRow(ctx, `SELECT string_agg(kind || '=' || n, ',' ORDER BY kind) FROM (SELECT kind, count(*) n FROM ai_cache GROUP BY kind) s`).Scan(&cache); err != nil {
		t.Fatal(err)
	}
	if cache != "dedup=2,embed=6,score=6" {
		t.Errorf("ai_cache = %s", cache)
	}

	// The neighbour query is served by the HNSW index when the planner can't seqscan.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	prow, err := tx.Query(ctx, `EXPLAIN SELECT id FROM items WHERE embedding IS NOT NULL ORDER BY embedding <=> $1::vector LIMIT 10`, postgres.VectorLiteral(fixture[0].vec))
	if err != nil {
		t.Fatal(err)
	}
	for prow.Next() {
		var line string
		_ = prow.Scan(&line)
		plan.WriteString(line + "\n")
	}
	if !strings.Contains(plan.String(), "items_embedding_hnsw_idx") {
		t.Errorf("neighbour query does not use the HNSW index:\n%s", plan.String())
	}
}

func TestVectorLiteralRoundTrip(t *testing.T) {
	v := []float32{0.5, -1.25, 3e-7}
	got, err := postgres.ParseVector(postgres.VectorLiteral(v))
	if err != nil || len(got) != 3 || got[0] != 0.5 || got[1] != -1.25 || got[2] != float32(3e-7) {
		t.Fatalf("round trip = %v, %v", got, err)
	}
}

// insertScored adds a scored, unclustered item with a fixed embedding.
func insertScored(t *testing.T, ctx context.Context, pool *pgxpool.Pool, srcID int64, key string, score float64, at time.Time, vec []float32) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx, `
INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, status, score, summary, embedding, published_at)
VALUES ($1, $2, $3, $3, $2, $4, 'scored', $5, 's', $6::vector, $7) RETURNING id`,
		srcID, key, "https://example.test/"+key, "title "+key, score, postgres.VectorLiteral(vec), at).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// rot returns the unit vector at angle deg in the (e0, e1) plane.
func rot(deg float64) []float32 {
	v := make([]float32, 768)
	r := deg * math.Pi / 180
	v[0], v[1] = float32(math.Cos(r)), float32(math.Sin(r))
	return v
}

func TestDedupBridgeMergesClusters(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	bus := testBus(t, ctx)
	src, err := postgres.SourceRepo{DB: pool}.Create(ctx, domain.SourceConfig{Kind: domain.KindRSS, Name: "bridge", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// P·Q = cos 40° = .766 (distinct); R·P = R·Q = cos 20° = .94 (direct).
	p := insertScored(t, ctx, pool, src.ID, "P", 5, now.Add(-3*time.Hour), rot(-20))
	q := insertScored(t, ctx, pool, src.ID, "Q", 8, now.Add(-2*time.Hour), rot(20))
	r := insertScored(t, ctx, pool, src.ID, "R", 6, now.Add(-time.Hour), rot(0))

	llm := &scriptedLLM{answer: `{"duplicates": []}`}
	ded := &Deduper{Repo: postgres.AIRepo{DB: pool}, Analyzer: analyzerWith(llm), Bus: bus}
	var res []postgres.ClusterResult
	for _, id := range []int64{p, q, r} {
		cr, err := ded.DedupItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		res = append(res, cr)
	}
	if res[0].ClusterID == res[1].ClusterID {
		t.Fatalf("P and Q must start in different clusters: %+v", res)
	}
	bridge := res[2]
	if bridge.Size != 3 || bridge.PrimaryID != q || len(bridge.Merged) != 1 {
		t.Fatalf("bridge result = %+v, want size 3, primary Q, one absorbed cluster", bridge)
	}
	if llm.calls != 0 {
		t.Errorf("no ambiguous pair, LLM calls = %d", llm.calls)
	}

	var clusters, members, count int
	if err := pool.QueryRow(ctx, `SELECT count(*), max(item_count) FROM clusters`).Scan(&clusters, &count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM items WHERE cluster_id = $1`, bridge.ClusterID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if clusters != 1 || count != 3 || members != 3 {
		t.Fatalf("clusters=%d item_count=%d members=%d, want 1/3/3", clusters, count, members)
	}
	var feed string
	if err := pool.QueryRow(ctx, `SELECT string_agg(item_id || ':' || duplicate_count || ':' || cluster_id, ',') FROM feed_view`).Scan(&feed); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%d:2:%d", q, bridge.ClusterID); feed != want {
		t.Errorf("feed_view = %s, want %s", feed, want)
	}
}

// flakyLLM always fails with err or answers raw (test-only).
type flakyLLM struct {
	err error
	raw string
}

func (f flakyLLM) Model() string { return "flaky" }
func (f flakyLLM) Chat(context.Context, ports.ChatRequest) (string, error) {
	return f.raw, f.err
}

func TestScoreFailuresCountOnlyInvalidOutput(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	bus := testBus(t, ctx)
	src, err := postgres.SourceRepo{DB: pool}.Create(ctx, domain.SourceConfig{Kind: domain.KindRSS, Name: "flaky", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	it := domain.Item{SourceID: src.ID, ExternalID: "x", URL: "https://example.test/x", Title: "x", PublishedAt: time.Now()}
	if err := it.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := (postgres.ItemRepo{DB: pool}).Upsert(ctx, it); err != nil {
		t.Fatal(err)
	}
	state := func() (string, int, string) {
		var st, last string
		var n int
		if err := pool.QueryRow(ctx, `SELECT status, score_attempts, last_error FROM items`).Scan(&st, &n, &last); err != nil {
			t.Fatal(err)
		}
		return st, n, last
	}
	scorer := func(llm ports.LLM) *Scorer {
		an := &app.Analyzer{LLM: llm, Embedder: fakeEmbed{&fakeModel{}}, Retry: retry.Policy{Attempts: 1}}
		return &Scorer{Repo: postgres.AIRepo{DB: pool}, Analyzer: an, Bus: bus, Workers: 1, MaxAttempts: 2}
	}

	// An outage records the error but keeps the attempts and the queue.
	if err := scorer(flakyLLM{err: errors.New("connection refused")}).Drain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if st, n, last := state(); st != "raw" || n != 0 || !strings.Contains(last, "connection refused") {
		t.Fatalf("after outage: status=%s attempts=%d last=%q", st, n, last)
	}
	// Invalid output spends attempts; at MaxAttempts the item leaves the queue.
	for range 2 {
		if err := scorer(flakyLLM{raw: "not json"}).Drain(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	if st, n, _ := state(); st != "failed" || n != 2 {
		t.Fatalf("after invalid outputs: status=%s attempts=%d, want failed/2", st, n)
	}
	// A failed item is not sent to the model again, even if a message names it.
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM items`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ok, err := scorer(flakyLLM{raw: `{"score": 5}`}).ScoreItem(ctx, id)
	if ok || err != nil {
		t.Fatalf("ScoreItem on failed item = %v, %v; want skipped", ok, err)
	}
}

func TestRunStagesContinuouslyAndStopCleanly(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	bus := testBus(t, ctx)
	src, err := postgres.SourceRepo{DB: pool}.Create(ctx, domain.SourceConfig{Kind: domain.KindRSS, Name: "live", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var raw []domain.Item
	for _, f := range fixture[1:4] { // A, B, C: one cluster
		it := domain.Item{SourceID: src.ID, ExternalID: f.key, URL: "https://example.test/" + f.key, Title: f.title, PublishedAt: now.Add(-f.ago)}
		if err := it.Prepare(); err != nil {
			t.Fatal(err)
		}
		if _, err := (postgres.ItemRepo{DB: pool}).Upsert(ctx, it); err != nil {
			t.Fatal(err)
		}
		raw = append(raw, it)
	}
	model := &fakeModel{}
	an := &app.Analyzer{LLM: model, Embedder: fakeEmbed{model}, Retry: retry.Policy{Attempts: 1}}
	ai := postgres.AIRepo{DB: pool}
	rawCons, err := bus.Consumer(ctx, "live-scorer", bus.RawSubject(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	scoredCons, err := bus.Consumer(ctx, "live-deduper", bus.ScoredSubject(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(ctx)
	errs := make(chan error, 2)
	go func() {
		errs <- (&Scorer{Repo: ai, Analyzer: an, Bus: bus, Workers: 2, BackfillEvery: 200 * time.Millisecond}).Run(runCtx, rawCons)
	}()
	go func() {
		errs <- (&Deduper{Repo: ai, Analyzer: an, Bus: bus, BackfillEvery: 200 * time.Millisecond}).Run(runCtx, scoredCons)
	}()
	// Items arrive on items.raw while the stages are running.
	if err := bus.PublishRaw(ctx, "live-1", raw); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM items WHERE cluster_id IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == len(raw) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d items clustered by the running stages", n, len(raw))
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("stage returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("stage did not stop after cancel")
		}
	}
	var clusters, feed int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM clusters), (SELECT count(*) FROM feed_view)`).Scan(&clusters, &feed); err != nil {
		t.Fatal(err)
	}
	if clusters != 1 || feed != 1 {
		t.Errorf("clusters=%d feed_view=%d, want 1/1", clusters, feed)
	}
}
