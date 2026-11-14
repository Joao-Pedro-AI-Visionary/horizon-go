package postgres

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/opsalerts"
	"github.com/jpdev/horizon-go/internal/retention"
	"github.com/jpdev/horizon-go/internal/scheduler"
	"github.com/jpdev/horizon-go/migrations"
)

const testSchedulerDBName = "horizon_test_scheduler"

func schedulerTestDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	adminDSN := config.DatabaseURL()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var exists bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, testSchedulerDBName).Scan(&exists); err != nil {
		t.Fatalf("postgres unreachable at %s: %v", adminDSN, err)
	}
	if !exists {
		if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+testSchedulerDBName); err != nil {
			t.Fatalf("create test db: %v", err)
		}
	}
	u, _ := url.Parse(adminDSN)
	u.Path = "/" + testSchedulerDBName
	mdb, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()
	if err := migrations.Run(mdb, "up"); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	pool, err := Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE sources, items, fetch_runs, clusters, feed_view, ai_cache, digests, llm_usage, alert_events, alert_cooldowns, scheduler_state RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	return pool
}

func TestSchedulerAdvisoryLockContentionAgainstPostgres(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool1 := schedulerTestDB(t, ctx)

	// Open a second independent pool (simulating a second container instance).
	u, _ := url.Parse(config.DatabaseURL())
	u.Path = "/" + testSchedulerDBName
	pool2, err := Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()

	repo1 := SchedulerRepo{DB: pool1}
	repo2 := SchedulerRepo{DB: pool2}

	start := make(chan struct{})
	var acquiredCount atomic.Int32
	var wg sync.WaitGroup
	var unlocks [2]func()

	for i, r := range []SchedulerRepo{repo1, repo2} {
		wg.Add(1)
		go func(idx int, repo SchedulerRepo) {
			defer wg.Done()
			<-start
			unlock, ok, err := repo.TryLock(ctx, scheduler.CollectLockID)
			if err != nil {
				t.Errorf("goroutine %d TryLock err: %v", idx, err)
				return
			}
			if ok {
				acquiredCount.Add(1)
				unlocks[idx] = unlock
			}
		}(i, r)
	}
	close(start)
	wg.Wait()

	if got := acquiredCount.Load(); got != 1 {
		t.Fatalf("concurrent TryLock acquired=%d want=1", got)
	}

	// Cleanup lock ID is independent from Collect lock ID.
	unlockCleanup, ok, err := repo2.TryLock(ctx, scheduler.CleanupLockID)
	if err != nil || !ok {
		t.Fatalf("independent cleanup lock failed: ok=%v err=%v", ok, err)
	}
	unlockCleanup()

	// Release holder lock (idempotent double-call must be safe).
	for _, u := range unlocks {
		if u != nil {
			u()
			u()
		}
	}

	// Now the second pool can acquire CollectLockID.
	unlock2, ok, err := repo2.TryLock(ctx, scheduler.CollectLockID)
	if err != nil || !ok {
		t.Fatalf("re-acquire after unlock failed: ok=%v err=%v", ok, err)
	}
	unlock2()
}

type testCollector struct {
	mu      sync.Mutex
	calls   int
	err     error
	blockCh chan struct{}
	waitCh  chan struct{}
}

func (c *testCollector) RunScheduledCollect(context.Context) (scheduler.CollectOutcome, error) {
	if c.blockCh != nil {
		close(c.blockCh)
		<-c.waitCh
	}
	c.mu.Lock()
	c.calls++
	err := c.err
	c.mu.Unlock()
	return scheduler.CollectOutcome{RunID: "pg-test-run", Fetched: 3, Published: 2}, err
}

func TestSchedulerEndToEndCatchUpAndGuardsAgainstPostgres(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := schedulerTestDB(t, ctx)

	loc, err := time.LoadLocation(scheduler.Timezone)
	if err != nil {
		t.Fatal(err)
	}

	// Seed 10 items: 2 older than 32 days (20% <= 80%), 8 fresh.
	var srcID int64
	if err := pool.QueryRow(ctx, `INSERT INTO sources (kind, name, config) VALUES ('push', 'sched test', '{}') RETURNING id`).Scan(&srcID); err != nil {
		t.Fatal(err)
	}
	installTime := time.Date(2026, 11, 14, 13, 0, 0, 0, loc)
	cutoff := installTime.AddDate(0, 0, -retention.RetentionDays)
	oldTime := cutoff.Add(-48 * time.Hour)
	freshTime := installTime.Add(-2 * time.Hour)
	insertItem := func(key string, at time.Time) {
		t.Helper()
		_, err := pool.Exec(ctx, `
INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, published_at, fetched_at)
VALUES ($1, $2, $3, $3, encode(sha256(convert_to($3, 'UTF8')), 'hex'), $2, $4, $4)`,
			srcID, key, "https://sched.example/"+key, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 2; i++ {
		insertItem("old-"+string(rune('a'+i)), oldTime)
	}
	for i := 1; i <= 8; i++ {
		insertItem("fresh-"+string(rune('a'+i)), freshTime)
	}

	repo := SchedulerRepo{DB: pool}
	// First boot at 13:05 on 2026-11-14:
	// - cleanup 01:00 today is BEFORE installTime -> must skip!
	// - collect 07:30 today is due -> must run once!
	if _, err := repo.EnsureJob(ctx, scheduler.JobCleanup, installTime); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureJob(ctx, scheduler.JobCollect, installTime); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 14, 13, 5, 0, 0, loc)
	col := &testCollector{}
	cleaner := retention.Service{Store: CleanupRepo{DB: pool}, Now: func() time.Time { return now }}
	svc := &scheduler.Service{
		Store:           repo,
		Collector:       col,
		Cleaner:         cleaner,
		CollectCooldown: 10 * time.Minute,
		Now:             func() time.Time { return now },
		Log:             slog.New(slog.DiscardHandler),
	}

	if err := svc.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if col.calls != 1 {
		t.Fatalf("first boot collect calls=%d want=1", col.calls)
	}
	var itemCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&itemCount); err != nil || itemCount != 10 {
		t.Fatalf("first boot must NOT delete old items (got %d, err=%v)", itemCount, err)
	}

	// Simulate manual collection at 16:25 -> 16:30 slot must skip via persisted cooldown.
	if err := repo.RecordManualCollect(ctx, time.Date(2026, 11, 14, 16, 25, 0, 0, loc)); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 14, 16, 30, 0, 0, loc)
	if err := svc.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if col.calls != 1 {
		t.Fatalf("persisted cooldown failed to block 16:30 catch-up: calls=%d", col.calls)
	}

	// Next morning at 08:00 (2026-11-15): two concurrent instances race CatchUp.
	// - cleanup 01:00 is AFTER installTime and 2/10 = 20% <= 80% -> deletes the 2 old items once.
	// - collect 07:30 -> runs once across both instances.
	now = time.Date(2026, 11, 15, 8, 0, 0, 0, loc)
	col2 := &testCollector{}
	svc2 := &scheduler.Service{
		Store:           repo,
		Collector:       col2,
		Cleaner:         cleaner,
		CollectCooldown: 10 * time.Minute,
		Now:             func() time.Time { return now },
		Log:             slog.New(slog.DiscardHandler),
	}
	var wg sync.WaitGroup
	for _, s := range []*scheduler.Service{svc, svc2} {
		wg.Add(1)
		go func(s *scheduler.Service) {
			defer wg.Done()
			if err := s.CatchUp(ctx); err != nil {
				t.Errorf("concurrent CatchUp err: %v", err)
			}
		}(s)
	}
	wg.Wait()

	if total := col.calls + col2.calls; total != 2 {
		t.Fatalf("expected exactly 1 additional collect across concurrent instances (total=%d)", total)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&itemCount); err != nil || itemCount != 8 {
		t.Fatalf("after 2026-11-15 01:00 catch-up expected 8 fresh items remaining, got %d (err=%v)", itemCount, err)
	}
}

type pgSpyMailer struct {
	mu       sync.Mutex
	subjects []string
}

func (m *pgSpyMailer) Send(_ context.Context, _, subject, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subjects = append(m.subjects, subject)
	return "re_pg_1", nil
}

func TestSchedulerOpsAlertsAndXIngestAgainstPostgres(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := schedulerTestDB(t, ctx)

	loc, err := time.LoadLocation(scheduler.Timezone)
	if err != nil {
		t.Fatal(err)
	}

	// Seed push source with stale last ingest (40h old) + 9 old items (>32d) out of 10 total
	// so retention guard triggers cleanup_blocked (90% > 80% threshold).
	var pushID int64
	if err := pool.QueryRow(ctx, `INSERT INTO sources (kind, name, config, enabled) VALUES ('push', 'X (radar)', '{}', true) RETURNING id`).Scan(&pushID); err != nil {
		t.Fatal(err)
	}
	installTime := time.Date(2026, 11, 13, 0, 0, 0, 0, loc)
	now := time.Date(2026, 11, 14, 16, 40, 0, 0, loc) // 16:40 -> 16:30 slot missed by 10m (>3m grace)
	staleX := now.Add(-40 * time.Hour)
	oldItem := now.AddDate(0, 0, -40)
	insert := func(key string, at time.Time) {
		t.Helper()
		_, err := pool.Exec(ctx, `
INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, published_at, fetched_at)
VALUES ($1, $2, $3, $3, encode(sha256(convert_to($3, 'UTF8')), 'hex'), $2, $4, $4)`,
			pushID, key, "https://x.example/"+key, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 9; i++ {
		insert("old-"+string(rune('0'+i)), oldItem)
	}
	insert("stale-1", staleX)
	if _, err := pool.Exec(ctx, `INSERT INTO fetch_runs (run_id, source_id, status, error, started_at, finished_at) VALUES ('ok', $1, 'ok', '', $2, $2)`, pushID, now); err != nil {
		t.Fatal(err)
	}

	read := ReadRepo{DB: pool}
	schedRepo := SchedulerRepo{DB: pool}
	if _, err := schedRepo.EnsureJob(ctx, scheduler.JobCleanup, installTime); err != nil {
		t.Fatal(err)
	}
	if _, err := schedRepo.EnsureJob(ctx, scheduler.JobCollect, installTime); err != nil {
		t.Fatal(err)
	}

	mailer := &pgSpyMailer{}
	ops := &opsalerts.Service{
		Store:    read,
		Mailer:   mailer,
		To:       "admin@example.invalid",
		Cooldown: 6 * time.Hour,
		Now:      func() time.Time { return now },
		Log:      slog.New(slog.DiscardHandler),
	}
	col := &testCollector{err: errors.New("collector postgres error")}
	cleaner := retention.Service{Store: CleanupRepo{DB: pool}, Now: func() time.Time { return now }}
	svc := &scheduler.Service{
		Store:     schedRepo,
		Collector: col,
		Cleaner:   cleaner,
		Alerter:   opsalerts.SchedulerBridge{Service: ops},
		XIngest:   read,
		Now:       func() time.Time { return now },
		Log:       slog.New(slog.DiscardHandler),
	}

	// CatchUp triggers:
	// - cleanup 01:00 overdue -> job_missed + cleanup_blocked
	// - collect 16:30 overdue -> job_missed (same 6h e-mail cooldown!) + job_failed
	_ = svc.CatchUp(ctx)
	if err := svc.CheckHealth(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify XIngestStatus returns expected stale state from real SQL query.
	xst, err := read.XIngestStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !xst.HasSource || !xst.HasEnabledSource || xst.LastIngestAt == nil || !xst.LastIngestAt.Equal(staleX) {
		t.Fatalf("unexpected XIngestStatus: %+v", xst)
	}

	// Check Postgres alert_cooldowns has exactly 4 kinds so far:
	// job_missed, cleanup_blocked, job_failed, x_ingest_stale.
	var cooldownCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM alert_cooldowns`).Scan(&cooldownCount); err != nil {
		t.Fatal(err)
	}
	if cooldownCount != 4 {
		t.Fatalf("alert_cooldowns count=%d, want 4", cooldownCount)
	}
	// Two job_missed triggers (cleanup & collect) sent only 1 job_missed email => total 4 emails.
	if len(mailer.subjects) != 4 {
		t.Fatalf("expected 4 distinct kind e-mails (job_missed coalesced by cooldown), got %d: %v", len(mailer.subjects), mailer.subjects)
	}

	// Collect recovers at next slot -> emits job_recovered (5th cooldown kind) and closes job_failed.
	now = time.Date(2026, 11, 15, 7, 30, 10, 0, loc)
	col.err = nil
	if err := svc.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := read.Alerts(ctx, now.Add(-48*time.Hour), now, 14*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, a := range list {
		kinds[a.Kind] = true
	}
	if kinds[domain.AlertJobFailed] {
		t.Fatalf("job_failed should have auto-closed after recovery: %+v", list)
	}
	for _, want := range []string{domain.AlertJobRecovered, domain.AlertJobMissed, domain.AlertCleanupBlocked, domain.AlertXIngestStale} {
		if !kinds[want] {
			t.Fatalf("missing expected badge alert %q in %+v", want, list)
		}
	}
}
