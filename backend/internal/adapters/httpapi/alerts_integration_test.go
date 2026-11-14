package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/digest"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/opsalerts"
)

// TestAlertsAgainstPostgres checks the alert queries on the real schema.
func TestAlertsAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	if _, err := pool.Exec(ctx, `TRUNCATE alert_events`); err != nil {
		t.Fatal(err)
	}
	read := postgres.ReadRepo{DB: pool}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO sources (id, kind, name, enabled) VALUES
 (1, 'rss', 'quebrada', true), (2, 'rss', 'recuperada', true), (3, 'rss', 'desligada', false), (4, 'rss', 'antiga', true)`)
	now := time.Now()
	run := func(src int, status, msg string, ago time.Duration) {
		at := now.Add(-ago)
		exec(`INSERT INTO fetch_runs (run_id, source_id, status, error, started_at, finished_at) VALUES ('r', $1, $2, $3, $4, $4)`, src, status, msg, at)
	}
	run(1, "error", "status 502 velho", 3*time.Hour)
	run(1, "error", "status 429 novo", time.Hour)
	run(2, "error", "status 500", 2*time.Hour)
	run(2, "ok", "", time.Hour)              // recovered: no alert
	run(3, "error", "status 404", time.Hour) // disabled: no alert
	run(4, "error", "fora da janela", 72*time.Hour)
	exec(`INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, status, last_error, updated_at) VALUES
 (2, 'a', 'https://e/a', 'e/a', 'ha', 'A', 'raw', 'groq 429', now() - interval '30 minutes'),
 (2, 'b', 'https://e/b', 'e/b', 'hb', 'B', 'failed', 'groq 400', now() - interval '20 minutes'),
 (2, 'c', 'https://e/c', 'e/c', 'hc', 'C', 'scored', '', now())`)

	svc := &digest.Service{Store: failingDigest{}, Failures: read}
	if _, err := svc.Generate(ctx, ""); err == nil {
		t.Fatal("want digest failure")
	}

	list, err := read.Alerts(ctx, now.Add(-48*time.Hour), now, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.Alert{}
	for _, a := range list {
		got[a.Kind+":"+a.Name] = a
	}
	if len(list) != 3 {
		t.Fatalf("want 3 alerts, got %+v", list)
	}
	if a := got["source:quebrada"]; a.Message != "status 429 novo" || a.SourceID == nil || *a.SourceID != 1 {
		t.Errorf("source alert %+v", a)
	}
	if a := got["scoring:recuperada"]; a.Message != "2 itens sem nota: groq 400" {
		t.Errorf("scoring alert %+v", a)
	}
	if a := got["digest:Digest"]; a.Message == "" {
		t.Errorf("digest alert missing: %+v", list)
	}

	// A digest saved afterwards closes the digest alert; an old last success
	// raises the collection alert.
	exec(`INSERT INTO digests (digest_date, title, markdown, updated_at) VALUES (CURRENT_DATE, 't', 'm', now() + interval '1 second')`)
	exec(`DELETE FROM fetch_runs WHERE status = 'ok'`)
	run(2, "ok", "", 20*time.Hour)
	list, err = read.Alerts(ctx, now.Add(-48*time.Hour), now.Add(2*time.Second), 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, a := range list {
		kinds[a.Kind]++
	}
	if kinds[domain.AlertDigest] != 0 || kinds[domain.AlertCollection] != 1 || kinds[domain.AlertSource] != 2 {
		t.Fatalf("after digest: %+v", list)
	}
}

type failingDigest struct{}

func (failingDigest) GetSettings(context.Context) (domain.Settings, error) {
	return domain.Settings{Timezone: "America/Sao_Paulo", DigestMaxItems: 5}, nil
}
func (failingDigest) DigestCandidates(context.Context, time.Time, time.Time) ([]domain.FeedItem, error) {
	return nil, context.DeadlineExceeded
}
func (failingDigest) SaveDigest(context.Context, domain.Digest) (domain.Digest, error) {
	return domain.Digest{}, nil
}

type spyMailer struct {
	mu   sync.Mutex
	sent int
}

func (m *spyMailer) Send(_ context.Context, _, _, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent++
	return "re_test_1", nil
}

// TestOpsAlertsClosingAndCooldownAgainstPostgres validates operational alert
// lifecycle in alert_events (job_failed/job_recovered, cleanup_blocked,
// x_ingest_stale) and concurrent cross-instance cooldown in alert_cooldowns.
func TestOpsAlertsClosingAndCooldownAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	if _, err := pool.Exec(ctx, `TRUNCATE sources, items, fetch_runs, clusters, feed_view, ai_cache, digests, llm_usage, alert_events, alert_cooldowns, scheduler_state RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	read := postgres.ReadRepo{DB: pool}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// Keep collection healthy so only our operational alerts are inspected.
	exec(`INSERT INTO sources (id, kind, name, enabled) VALUES (1, 'rss', 'Feed OK', true), (2, 'push', 'X (radar)', true)`)
	t0 := time.Now().UTC().Truncate(time.Second)
	exec(`INSERT INTO fetch_runs (run_id, source_id, status, error, started_at, finished_at) VALUES ('ok1', 1, 'ok', '', $1, $1)`, t0)

	// 1. job_failed opens -> job_recovered closes it -> newer job_failed closes job_recovered.
	if err := read.RecordAlertEvent(ctx, domain.AlertJobFailed, "Coleta", "boom 1", t0.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err := read.Alerts(ctx, t0.Add(-2*time.Hour), t0, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != domain.AlertJobFailed {
		t.Fatalf("expected open job_failed, got %+v", list)
	}

	if err := read.RecordAlertEvent(ctx, domain.AlertJobRecovered, "Coleta", "back online", t0.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err = read.Alerts(ctx, t0.Add(-2*time.Hour), t0, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != domain.AlertJobRecovered {
		t.Fatalf("expected job_recovered closing job_failed, got %+v", list)
	}

	if err := read.RecordAlertEvent(ctx, domain.AlertJobFailed, "Coleta", "boom 2", t0.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err = read.Alerts(ctx, t0.Add(-2*time.Hour), t0, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != domain.AlertJobFailed || list[0].Message != "boom 2" {
		t.Fatalf("expected newer job_failed closing job_recovered, got %+v", list)
	}

	// 2. cleanup_blocked stays open on 'skipped', closes only on cleanup 'ok' after event time.
	exec(`INSERT INTO scheduler_state (job_name, installed_at, last_status, updated_at) VALUES ('cleanup', $1, 'skipped', $2)`,
		t0.Add(-48*time.Hour), t0.Add(-5*time.Minute))
	if err := read.RecordAlertEvent(ctx, domain.AlertCleanupBlocked, "Limpeza", "below_floor_total", t0.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err = read.Alerts(ctx, t0.Add(-2*time.Hour), t0, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	hasKind := func(items []domain.Alert, k string) bool {
		for _, a := range items {
			if a.Kind == k {
				return true
			}
		}
		return false
	}
	if !hasKind(list, domain.AlertCleanupBlocked) {
		t.Fatalf("expected cleanup_blocked while status='skipped': %+v", list)
	}
	exec(`UPDATE scheduler_state SET last_status = 'ok', last_success_at = $1 WHERE job_name = 'cleanup'`, t0.Add(-time.Minute))
	list, err = read.Alerts(ctx, t0.Add(-2*time.Hour), t0, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if hasKind(list, domain.AlertCleanupBlocked) {
		t.Fatalf("cleanup_blocked should close after cleanup ok: %+v", list)
	}

	// 3. x_ingest_stale closes once a newer push item arrives.
	if err := read.RecordAlertEvent(ctx, domain.AlertXIngestStale, "X (radar)", "stale 40h", t0.Add(-4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err = read.Alerts(ctx, t0.Add(-2*time.Hour), t0, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind(list, domain.AlertXIngestStale) {
		t.Fatalf("expected open x_ingest_stale: %+v", list)
	}
	exec(`INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, status, fetched_at) VALUES
 (2, 'x1', 'https://x.com/1', 'x.com/1', 'hx1', 'Post X', 'raw', $1)`, t0.Add(-time.Minute))
	list, err = read.Alerts(ctx, t0.Add(-2*time.Hour), t0, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if hasKind(list, domain.AlertXIngestStale) {
		t.Fatalf("x_ingest_stale should close after fresh push item: %+v", list)
	}

	// 4. Concurrent ClaimEmailCooldown cannot be pierced across parallel callers.
	const workers = 8
	start := make(chan struct{})
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed int
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, cerr := read.ClaimEmailCooldown(ctx, domain.AlertJobFailed, t0, 6*time.Hour)
			if cerr != nil {
				t.Errorf("claim: %v", cerr)
				return
			}
			if ok {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if claimed != 1 {
		t.Fatalf("concurrent ClaimEmailCooldown claimed=%d, want 1", claimed)
	}

	// 5. POST /api/admin/alerts/test against real Postgres records badge and respects cooldown.
	mailer := &spyMailer{}
	nowRef := t0
	ops := &opsalerts.Service{
		Store:    read,
		Mailer:   mailer,
		To:       "admin@example.invalid",
		Cooldown: 6 * time.Hour,
		Now:      func() time.Time { return nowRef },
	}
	srv := &Server{
		Store:        read,
		Sources:      &fakeSources{},
		Collector:    &countingCollector{},
		Alerts:       read,
		AlertTester:  ops,
		AdminToken:   "static-token",
		RequireAdmin: true,
	}
	h := srv.Handler()
	rec1 := doAuth(h, http.MethodPost, "/api/admin/alerts/test", "Bearer static-token", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("test alert 1: %d %s", rec1.Code, rec1.Body.String())
	}
	var out1 testAlertDTO
	if err := json.Unmarshal(rec1.Body.Bytes(), &out1); err != nil || !out1.Emailed || !out1.Recorded {
		t.Fatalf("unexpected test 1 response: %+v (%v)", out1, err)
	}
	// Second test 5 minutes later: badge recorded, e-mail suppressed by Postgres cooldown.
	nowRef = t0.Add(5 * time.Minute)
	rec2 := doAuth(h, http.MethodPost, "/api/admin/alerts/test", "Bearer static-token", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("test alert 2: %d %s", rec2.Code, rec2.Body.String())
	}
	var out2 testAlertDTO
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil || out2.Emailed || !out2.Suppressed || !out2.Recorded {
		t.Fatalf("unexpected test 2 response: %+v (%v)", out2, err)
	}
	if mailer.sent != 1 {
		t.Fatalf("mailer sent=%d, want 1", mailer.sent)
	}
}
