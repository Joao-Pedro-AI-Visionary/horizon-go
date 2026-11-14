package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/retention"
)

type memStore struct {
	mu      sync.Mutex
	jobs    map[string]JobState
	locks   map[int64]bool
	records []string
}

func newMemStore() *memStore {
	return &memStore{
		jobs:  map[string]JobState{},
		locks: map[int64]bool{},
	}
}

func (m *memStore) EnsureJob(_ context.Context, job string, installedAt time.Time) (JobState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.jobs[job]; ok {
		return st, nil
	}
	st := JobState{JobName: job, InstalledAt: installedAt}
	m.jobs[job] = st
	return st, nil
}

func (m *memStore) RecordRun(_ context.Context, job string, rec RunRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.jobs[job]
	st.JobName = job
	r := rec.RunAt
	st.LastRunAt = &r
	st.LastStatus = rec.Status
	st.LastError = rec.Error
	if rec.SlotAt != nil {
		s := *rec.SlotAt
		st.LastSlotAt = &s
	}
	if rec.SuccessAt != nil {
		s := *rec.SuccessAt
		st.LastSuccessAt = &s
	}
	m.jobs[job] = st
	m.records = append(m.records, job+":"+rec.Status)
	return nil
}

func (m *memStore) TryLock(_ context.Context, key int64) (func(), bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[key] {
		return nil, false, nil
	}
	m.locks[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			delete(m.locks, key)
			m.mu.Unlock()
		})
	}, true, nil
}

type fakeCollector struct {
	mu    sync.Mutex
	calls int
	out   CollectOutcome
	err   error
	hook  func()
}

func (f *fakeCollector) RunScheduledCollect(context.Context) (CollectOutcome, error) {
	f.mu.Lock()
	f.calls++
	hook := f.hook
	out := f.out
	err := f.err
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if out.RunID == "" && !out.Skipped && err == nil {
		out.RunID = "run-1"
	}
	return out, err
}

type fakeCleaner struct {
	mu      sync.Mutex
	calls   []bool
	dry     retention.Report
	real    retention.Report
	dryErr  error
	realErr error
	hook    func(dryRun bool)
}

func (f *fakeCleaner) Cleanup(_ context.Context, dryRun bool) (retention.Report, error) {
	f.mu.Lock()
	f.calls = append(f.calls, dryRun)
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		hook(dryRun)
	}
	if dryRun {
		return f.dry, f.dryErr
	}
	return f.real, f.realErr
}

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(Timezone)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestSlotCalculationInSaoPaulo(t *testing.T) {
	loc := mustLoc(t)
	cases := []struct {
		name        string
		now         time.Time
		wantCollect time.Time
		wantCleanup time.Time
		wantNext    scheduledEvent
	}{
		{
			name:        "before 01:00",
			now:         time.Date(2026, 11, 14, 0, 30, 0, 0, loc),
			wantCollect: time.Date(2026, 11, 13, 16, 30, 0, 0, loc),
			wantCleanup: time.Date(2026, 11, 13, 1, 0, 0, 0, loc),
			wantNext:    scheduledEvent{Job: JobCleanup, Slot: time.Date(2026, 11, 14, 1, 0, 0, 0, loc)},
		},
		{
			name:        "between 01:00 and 07:30",
			now:         time.Date(2026, 11, 14, 4, 0, 0, 0, loc),
			wantCollect: time.Date(2026, 11, 13, 16, 30, 0, 0, loc),
			wantCleanup: time.Date(2026, 11, 14, 1, 0, 0, 0, loc),
			wantNext:    scheduledEvent{Job: JobCollect, Slot: time.Date(2026, 11, 14, 7, 30, 0, 0, loc)},
		},
		{
			name:        "exact 07:30",
			now:         time.Date(2026, 11, 14, 7, 30, 0, 0, loc),
			wantCollect: time.Date(2026, 11, 14, 7, 30, 0, 0, loc),
			wantCleanup: time.Date(2026, 11, 14, 1, 0, 0, 0, loc),
			wantNext:    scheduledEvent{Job: JobCollect, Slot: time.Date(2026, 11, 14, 16, 30, 0, 0, loc)},
		},
		{
			name:        "afternoon 13:05",
			now:         time.Date(2026, 11, 14, 13, 5, 0, 0, loc),
			wantCollect: time.Date(2026, 11, 14, 7, 30, 0, 0, loc),
			wantCleanup: time.Date(2026, 11, 14, 1, 0, 0, 0, loc),
			wantNext:    scheduledEvent{Job: JobCollect, Slot: time.Date(2026, 11, 14, 16, 30, 0, 0, loc)},
		},
		{
			name:        "after 16:30",
			now:         time.Date(2026, 11, 14, 20, 0, 0, 0, loc),
			wantCollect: time.Date(2026, 11, 14, 16, 30, 0, 0, loc),
			wantCleanup: time.Date(2026, 11, 14, 1, 0, 0, 0, loc),
			wantNext:    scheduledEvent{Job: JobCleanup, Slot: time.Date(2026, 11, 15, 1, 0, 0, 0, loc)},
		},
	}

	s := &Service{Collector: &fakeCollector{}, Cleaner: &fakeCleaner{}}
	for _, tc := range cases {
		if got := LatestSlot(tc.now, loc, DefaultCollectSlots); !got.Equal(tc.wantCollect) {
			t.Errorf("%s latest collect = %v, want %v", tc.name, got, tc.wantCollect)
		}
		if got := LatestSlot(tc.now, loc, DefaultCleanupSlots); !got.Equal(tc.wantCleanup) {
			t.Errorf("%s latest cleanup = %v, want %v", tc.name, got, tc.wantCleanup)
		}
		ev, ok := s.nextEvent(tc.now, loc)
		if !ok || ev.Job != tc.wantNext.Job || !ev.Slot.Equal(tc.wantNext.Slot) {
			t.Errorf("%s next event = (%v, %v), want (%v, %v)", tc.name, ev.Job, ev.Slot, tc.wantNext.Job, tc.wantNext.Slot)
		}
	}
}

func TestFirstBootAfterInstallSkipsPreInstallCleanupAndCatchesUpCollectOnce(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 11, 14, 13, 10, 0, 0, loc)
	st := newMemStore()
	// Installed at 13:10 today; 01:00 today must be skipped safely!
	_, _ = st.EnsureJob(context.Background(), JobCleanup, now)
	_, _ = st.EnsureJob(context.Background(), JobCollect, now)

	col := &fakeCollector{out: CollectOutcome{RunID: "r-boot", Fetched: 10, Published: 5}}
	cln := &fakeCleaner{
		dry: retention.Report{Counts: retention.Counts{"items": 2, "items_total": 50, "feed_view": 1}},
	}
	var buf bytes.Buffer
	svc := &Service{
		Store:           st,
		Collector:       col,
		Cleaner:         cln,
		CollectCooldown: 10 * time.Minute,
		Now:             func() time.Time { return now },
		Log:             slog.New(slog.NewTextHandler(&buf, nil)),
	}

	if err := svc.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cln.calls) != 0 {
		t.Fatalf("cleanup must skip pre-install slot 01:00, got calls=%v", cln.calls)
	}
	if col.calls != 1 {
		t.Fatalf("collect must catch up 07:30 once, got calls=%d", col.calls)
	}
	if !strings.Contains(buf.String(), "slot_before_install") {
		t.Fatalf("expected slot_before_install log, got:\n%s", buf.String())
	}

	// Second boot 3 minutes later: slot already completed + cooldown -> no repeat.
	now = now.Add(3 * time.Minute)
	if err := svc.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if col.calls != 1 || len(cln.calls) != 0 {
		t.Fatalf("second catch-up re-ran jobs: collect=%d cleanup=%v", col.calls, cln.calls)
	}
}

func TestCleanupSafetyGuardsInScheduler(t *testing.T) {
	loc := mustLoc(t)
	installedAt := time.Date(2026, 11, 14, 13, 0, 0, 0, loc)

	cases := []struct {
		name       string
		counts     retention.Counts
		wantCalls  []bool
		wantStatus string
	}{
		{
			name:       "zero items skips real delete",
			counts:     retention.Counts{"items": 0, "items_total": 100, "feed_view": 0},
			wantCalls:  []bool{true},
			wantStatus: "skipped",
		},
		{
			name:       "unsafe >80pct blocks real delete",
			counts:     retention.Counts{"items": 85, "items_total": 100, "feed_view": 10},
			wantCalls:  []bool{true},
			wantStatus: "blocked",
		},
		{
			name:       "unsafe feed_view>items blocks real delete",
			counts:     retention.Counts{"items": 5, "items_total": 100, "feed_view": 6},
			wantCalls:  []bool{true},
			wantStatus: "blocked",
		},
		{
			name:       "safe set runs dry-run then real delete",
			counts:     retention.Counts{"items": 10, "items_total": 100, "feed_view": 4},
			wantCalls:  []bool{true, false},
			wantStatus: "ok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newMemStore()
			_, _ = st.EnsureJob(context.Background(), JobCleanup, installedAt)
			cln := &fakeCleaner{
				dry:  retention.Report{DryRun: true, Counts: tc.counts},
				real: retention.Report{DryRun: false, Counts: tc.counts},
			}
			now := time.Date(2026, 11, 15, 1, 0, 0, 0, loc)
			svc := &Service{
				Store:   st,
				Cleaner: cln,
				Now:     func() time.Time { return now },
				Log:     slog.New(slog.DiscardHandler),
			}
			if err := svc.CatchUp(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(cln.calls) != len(tc.wantCalls) {
				t.Fatalf("calls=%v want=%v", cln.calls, tc.wantCalls)
			}
			for i := range tc.wantCalls {
				if cln.calls[i] != tc.wantCalls[i] {
					t.Fatalf("call[%d]=%v want=%v", i, cln.calls[i], tc.wantCalls[i])
				}
			}
			if got := st.jobs[JobCleanup].LastStatus; got != tc.wantStatus {
				t.Fatalf("status=%q want=%q", got, tc.wantStatus)
			}
		})
	}
}

func TestCollectAndCleanupNeverRunConcurrentlyInCatchUp(t *testing.T) {
	loc := mustLoc(t)
	installedAt := time.Date(2026, 11, 14, 13, 0, 0, 0, loc)
	now := time.Date(2026, 11, 15, 8, 0, 0, 0, loc) // both 01:00 and 07:30 overdue

	st := newMemStore()
	_, _ = st.EnsureJob(context.Background(), JobCleanup, installedAt)
	_, _ = st.EnsureJob(context.Background(), JobCollect, installedAt)

	var mu sync.Mutex
	var events []string
	cln := &fakeCleaner{
		dry:  retention.Report{DryRun: true, Counts: retention.Counts{"items": 1, "items_total": 10, "feed_view": 1}},
		real: retention.Report{DryRun: false, Counts: retention.Counts{"items": 1}},
		hook: func(dryRun bool) {
			mu.Lock()
			defer mu.Unlock()
			if dryRun {
				events = append(events, "cleanup-dry")
			} else {
				events = append(events, "cleanup-real")
			}
		},
	}
	col := &fakeCollector{
		hook: func() {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "collect")
		},
	}
	svc := &Service{
		Store:     st,
		Collector: col,
		Cleaner:   cln,
		Now:       func() time.Time { return now },
		Log:       slog.New(slog.DiscardHandler),
	}
	if err := svc.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "cleanup-dry,cleanup-real,collect"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events=%q want=%q", got, want)
	}
}

func TestPersistedCooldownAndAdvisoryLockSkip(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 11, 15, 7, 35, 0, 0, loc)
	st := newMemStore()
	recent := now.Add(-4 * time.Minute)
	st.jobs[JobCollect] = JobState{
		JobName:     JobCollect,
		InstalledAt: now.Add(-24 * time.Hour),
		LastRunAt:   &recent,
	}
	col := &fakeCollector{}
	svc := &Service{
		Store:           st,
		Collector:       col,
		CollectCooldown: 10 * time.Minute,
		Now:             func() time.Time { return now },
		Log:             slog.New(slog.DiscardHandler),
	}
	if err := svc.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if col.calls != 0 {
		t.Fatalf("cooldown must skip catch-up, got calls=%d", col.calls)
	}

	// Simulate another instance holding the advisory lock.
	unlock, ok, err := st.TryLock(context.Background(), CollectLockID)
	if err != nil || !ok {
		t.Fatalf("pre-lock failed: ok=%v err=%v", ok, err)
	}
	now = now.Add(15 * time.Minute) // cooldown expired
	if err := svc.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if col.calls != 0 {
		t.Fatalf("advisory lock held must skip catch-up, got calls=%d", col.calls)
	}
	unlock()

	// Once lock is released, catch-up succeeds.
	if err := svc.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if col.calls != 1 {
		t.Fatalf("after lock release expected 1 collect, got %d", col.calls)
	}
}

func TestCollectErrorDoesNotMarkSlotCompleted(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 11, 15, 7, 35, 0, 0, loc)
	st := newMemStore()
	col := &fakeCollector{err: errors.New("nats down")}
	svc := &Service{
		Store:           st,
		Collector:       col,
		CollectCooldown: 10 * time.Minute,
		Now:             func() time.Time { return now },
		Log:             slog.New(slog.DiscardHandler),
	}
	if err := svc.CatchUp(context.Background()); err == nil {
		t.Fatal("expected error from CatchUp")
	}
	got := st.jobs[JobCollect]
	if got.LastStatus != "error" || got.LastSlotAt != nil || got.LastSuccessAt != nil || got.LastRunAt == nil {
		t.Fatalf("unexpected state after error: %+v", got)
	}
}

func TestRunLoopFiresScheduledEventAndStopsCleanly(t *testing.T) {
	loc := mustLoc(t)
	startReal := time.Now()
	baseFake := time.Date(2026, 11, 15, 7, 29, 59, 920_000_000, loc) // 80ms before 07:30:00
	yesterdayCollect := time.Date(2026, 11, 14, 7, 30, 0, 0, loc)
	yesterdayCleanup := time.Date(2026, 11, 15, 1, 0, 0, 0, loc)

	st := newMemStore()
	st.jobs[JobCollect] = JobState{
		JobName:     JobCollect,
		InstalledAt: yesterdayCollect,
		LastSlotAt:  &yesterdayCollect,
	}
	st.jobs[JobCleanup] = JobState{
		JobName:     JobCleanup,
		InstalledAt: yesterdayCollect,
		LastSlotAt:  &yesterdayCleanup,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	fired := make(chan struct{}, 1)
	col := &fakeCollector{
		hook: func() {
			select {
			case fired <- struct{}{}:
			default:
			}
			cancel()
		},
	}
	cln := &fakeCleaner{}
	svc := &Service{
		Store:        st,
		Collector:    col,
		Cleaner:      cln,
		CollectSlots: []TimeOfDay{{Hour: 7, Minute: 30}},
		CleanupSlots: []TimeOfDay{{Hour: 1, Minute: 0}},
		Now:          func() time.Time { return baseFake.Add(time.Since(startReal)) },
		Log:          slog.New(slog.DiscardHandler),
	}

	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled 07:30 collect event did not fire")
	}

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if col.calls != 1 {
		t.Fatalf("collect calls=%d, want 1", col.calls)
	}
	if len(cln.calls) != 0 {
		t.Fatalf("unexpected cleanup calls=%v", cln.calls)
	}
	wantSlot := time.Date(2026, 11, 15, 7, 30, 0, 0, loc)
	if got := st.jobs[JobCollect].LastSlotAt; got == nil || !got.Equal(wantSlot) {
		t.Fatalf("last_slot_at=%v, want %v", got, wantSlot)
	}
}

type recordedAlert struct {
	kind       string
	job        string
	slot       time.Time
	at         time.Time
	err        error
	viaCatchUp bool
	dec        retention.DeleteDecision
	lastIngest *time.Time
	anchor     *time.Time
}

type recordingAlerter struct {
	mu     sync.Mutex
	events []recordedAlert
}

func (a *recordingAlerter) record(ev recordedAlert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
}

func (a *recordingAlerter) JobFailed(_ context.Context, job string, slot, at time.Time, err error) {
	a.record(recordedAlert{kind: "job_failed", job: job, slot: slot, at: at, err: err})
}
func (a *recordingAlerter) JobRecovered(_ context.Context, job string, slot, at time.Time) {
	a.record(recordedAlert{kind: "job_recovered", job: job, slot: slot, at: at})
}
func (a *recordingAlerter) JobMissed(_ context.Context, job string, slot, at time.Time, viaCatchUp bool) {
	a.record(recordedAlert{kind: "job_missed", job: job, slot: slot, at: at, viaCatchUp: viaCatchUp})
}
func (a *recordingAlerter) CleanupBlocked(_ context.Context, slot, at time.Time, dec retention.DeleteDecision) {
	a.record(recordedAlert{kind: "cleanup_blocked", job: JobCleanup, slot: slot, at: at, dec: dec})
}
func (a *recordingAlerter) XIngestStale(_ context.Context, lastIngestAt, anchorAt *time.Time, at time.Time) {
	a.record(recordedAlert{kind: "x_ingest_stale", at: at, lastIngest: lastIngestAt, anchor: anchorAt})
}

type fakeXIngestStore struct {
	state XIngestState
	err   error
}

func (f *fakeXIngestStore) XIngestStatus(context.Context) (XIngestState, error) {
	return f.state, f.err
}

func TestSchedulerAlertsJobFailedAndRecovered(t *testing.T) {
	loc := mustLoc(t)
	installed := time.Date(2026, 11, 13, 0, 0, 0, 0, loc)
	now := time.Date(2026, 11, 14, 7, 30, 30, 0, loc)
	st := newMemStore()
	st.jobs[JobCollect] = JobState{JobName: JobCollect, InstalledAt: installed}
	st.jobs[JobCleanup] = JobState{JobName: JobCleanup, InstalledAt: installed}

	al := &recordingAlerter{}
	col := &fakeCollector{err: errors.New("db timeout")}
	cln := &fakeCleaner{dryErr: errors.New("dry_run failed")}
	svc := &Service{
		Store:     st,
		Collector: col,
		Cleaner:   cln,
		Alerter:   al,
		Now:       func() time.Time { return now },
		Log:       slog.New(slog.DiscardHandler),
	}

	// 1. Cleanup dry-run fails & collect fails (both on time) -> 2 job_failed alerts.
	now = time.Date(2026, 11, 14, 1, 0, 10, 0, loc)
	if err := svc.runCleanupSlot(context.Background(), time.Date(2026, 11, 14, 1, 0, 0, 0, loc), TriggerSchedule); err == nil {
		t.Fatal("want cleanup error")
	}
	now = time.Date(2026, 11, 14, 7, 30, 10, 0, loc)
	if err := svc.runCollectSlot(context.Background(), time.Date(2026, 11, 14, 7, 30, 0, 0, loc), TriggerSchedule); err == nil {
		t.Fatal("want collect error")
	}
	if len(al.events) != 2 || al.events[0].kind != "job_failed" || al.events[0].job != JobCleanup || al.events[1].kind != "job_failed" || al.events[1].job != JobCollect {
		t.Fatalf("unexpected alerts after failures: %+v", al.events)
	}

	// 2. Next run: collect succeeds, cleanup skipped (0 candidates) -> 2 job_recovered alerts.
	al.events = nil
	col.err = nil
	cln.dryErr = nil
	cln.dry = retention.Report{Counts: retention.Counts{"items_total": 100, "feed_view": 50, "items": 0}}
	now = time.Date(2026, 11, 14, 16, 30, 10, 0, loc)
	if err := svc.runCollectSlot(context.Background(), time.Date(2026, 11, 14, 16, 30, 0, 0, loc), TriggerSchedule); err != nil {
		t.Fatalf("collect recovery: %v", err)
	}
	now = time.Date(2026, 11, 15, 1, 0, 10, 0, loc)
	if err := svc.runCleanupSlot(context.Background(), time.Date(2026, 11, 15, 1, 0, 0, 0, loc), TriggerSchedule); err != nil {
		t.Fatalf("cleanup skipped recovery: %v", err)
	}
	if len(al.events) != 2 || al.events[0].kind != "job_recovered" || al.events[0].job != JobCollect || al.events[1].kind != "job_recovered" || al.events[1].job != JobCleanup {
		t.Fatalf("expected 2 job_recovered alerts, got %+v", al.events)
	}

	// 3. Repeated success must NOT emit another job_recovered.
	al.events = nil
	now = time.Date(2026, 11, 15, 7, 30, 10, 0, loc)
	if err := svc.runCollectSlot(context.Background(), time.Date(2026, 11, 15, 7, 30, 0, 0, loc), TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if len(al.events) != 0 {
		t.Fatalf("unexpected alert on repeated success: %+v", al.events)
	}

	// 4. Cleanup real-delete fails then recovers -> job_failed then job_recovered.
	al.events = nil
	cln.dry = retention.Report{Counts: retention.Counts{"items_total": 200, "feed_view": 10, "items": 10}}
	cln.realErr = errors.New("deadlock detected")
	now = time.Date(2026, 11, 16, 1, 0, 10, 0, loc)
	if err := svc.runCleanupSlot(context.Background(), time.Date(2026, 11, 16, 1, 0, 0, 0, loc), TriggerSchedule); err == nil {
		t.Fatal("want delete error")
	}
	if len(al.events) != 1 || al.events[0].kind != "job_failed" || al.events[0].job != JobCleanup {
		t.Fatalf("expected cleanup job_failed on delete err, got %+v", al.events)
	}
	al.events = nil
	cln.realErr = nil
	now = time.Date(2026, 11, 17, 1, 0, 10, 0, loc)
	if err := svc.runCleanupSlot(context.Background(), time.Date(2026, 11, 17, 1, 0, 0, 0, loc), TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if len(al.events) != 1 || al.events[0].kind != "job_recovered" || al.events[0].job != JobCleanup {
		t.Fatalf("expected cleanup job_recovered after real delete, got %+v", al.events)
	}
}

func TestSchedulerAlertsCleanupBlockedGuard(t *testing.T) {
	loc := mustLoc(t)
	installed := time.Date(2026, 11, 13, 0, 0, 0, 0, loc)
	slot1 := time.Date(2026, 11, 14, 1, 0, 0, 0, loc)
	slot2 := time.Date(2026, 11, 15, 1, 0, 0, 0, loc)

	st := newMemStore()
	st.jobs[JobCleanup] = JobState{JobName: JobCleanup, InstalledAt: installed}
	al := &recordingAlerter{}
	cln := &fakeCleaner{dry: retention.Report{Counts: retention.Counts{"items_total": 100, "feed_view": 40, "items": 85}}}

	now := slot1.Add(time.Minute)
	svc := &Service{Store: st, Cleaner: cln, Alerter: al, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
	if err := svc.runCleanupSlot(context.Background(), slot1, TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if len(al.events) != 1 || al.events[0].kind != "cleanup_blocked" {
		t.Fatalf("expected cleanup_blocked alert, got %+v", al.events)
	}
	dec := al.events[0].dec
	if dec.Reason != "candidates_gt_80pct" || dec.Items != 85 || dec.Total != 100 || dec.Feed != 40 {
		t.Fatalf("unexpected blocked decision: %+v", dec)
	}

	// No-op (Items == 0) is normal skipped state, NOT cleanup_blocked.
	al.events = nil
	cln.dry = retention.Report{Counts: retention.Counts{"items_total": 100, "feed_view": 0, "items": 0}}
	now = slot2.Add(time.Minute)
	if err := svc.runCleanupSlot(context.Background(), slot2, TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if len(al.events) != 0 {
		t.Fatalf("no_candidates should not raise cleanup_blocked: %+v", al.events)
	}
}

func TestSchedulerAlertsJobMissedCatchUpAndOverdue(t *testing.T) {
	loc := mustLoc(t)
	installed := time.Date(2026, 11, 14, 7, 0, 0, 0, loc)
	slot0730 := time.Date(2026, 11, 14, 7, 30, 0, 0, loc)

	// 1. Pre-install slot during first-boot collect catch-up must NOT alert job_missed.
	st := newMemStore()
	firstBootAt := time.Date(2026, 11, 14, 13, 10, 0, 0, loc)
	st.jobs[JobCollect] = JobState{JobName: JobCollect, InstalledAt: firstBootAt}
	al := &recordingAlerter{}
	svc := &Service{
		Store:     st,
		Collector: &fakeCollector{},
		Alerter:   al,
		Now:       func() time.Time { return firstBootAt },
		Log:       slog.New(slog.DiscardHandler),
	}
	if err := svc.runCollectSlot(context.Background(), slot0730, TriggerBootCatchup); err != nil {
		t.Fatal(err)
	}
	if len(al.events) != 0 {
		t.Fatalf("pre-install slot should not emit job_missed: %+v", al.events)
	}

	// 2. Within 3-minute catch-up grace must NOT alert job_missed.
	st2 := newMemStore()
	st2.jobs[JobCollect] = JobState{JobName: JobCollect, InstalledAt: installed}
	al2 := &recordingAlerter{}
	svc2 := &Service{
		Store:     st2,
		Collector: &fakeCollector{},
		Alerter:   al2,
		Now:       func() time.Time { return slot0730.Add(2 * time.Minute) },
		Log:       slog.New(slog.DiscardHandler),
	}
	if err := svc2.runCollectSlot(context.Background(), slot0730, TriggerBootCatchup); err != nil {
		t.Fatal(err)
	}
	if len(al2.events) != 0 {
		t.Fatalf("within grace should not emit job_missed: %+v", al2.events)
	}

	// 3. Catch-up beyond 3m grace emits job_missed with viaCatchUp=true.
	st3 := newMemStore()
	st3.jobs[JobCollect] = JobState{JobName: JobCollect, InstalledAt: installed}
	al3 := &recordingAlerter{}
	svc3 := &Service{
		Store:     st3,
		Collector: &fakeCollector{},
		Alerter:   al3,
		Now:       func() time.Time { return slot0730.Add(20 * time.Minute) },
		Log:       slog.New(slog.DiscardHandler),
	}
	if err := svc3.runCollectSlot(context.Background(), slot0730, TriggerBootCatchup); err != nil {
		t.Fatal(err)
	}
	if len(al3.events) != 1 || al3.events[0].kind != "job_missed" || !al3.events[0].viaCatchUp || !al3.events[0].slot.Equal(slot0730) {
		t.Fatalf("expected catch-up job_missed, got %+v", al3.events)
	}

	// 4. CheckHealth raises job_missed (viaCatchUp=false) when slot > installed_at and overdue > 3h.
	st4 := newMemStore()
	st4.jobs[JobCollect] = JobState{JobName: JobCollect, InstalledAt: installed}
	al4 := &recordingAlerter{}
	svc4 := &Service{
		Store:     st4,
		Collector: &fakeCollector{},
		Alerter:   al4,
		Now:       func() time.Time { return slot0730.Add(3*time.Hour + time.Minute) },
		Log:       slog.New(slog.DiscardHandler),
	}
	if err := svc4.CheckHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(al4.events) != 1 || al4.events[0].kind != "job_missed" || al4.events[0].viaCatchUp {
		t.Fatalf("expected overdue job_missed, got %+v", al4.events)
	}

	// 5. Completed slot does NOT raise CheckHealth job_missed.
	al4.events = nil
	st4.jobs[JobCollect] = JobState{JobName: JobCollect, InstalledAt: installed, LastSlotAt: &slot0730}
	if err := svc4.CheckHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(al4.events) != 0 {
		t.Fatalf("completed slot should not trigger overdue job_missed: %+v", al4.events)
	}
}

func TestEvaluateXIngestStaleAndCheckHealth(t *testing.T) {
	now := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	ptr := func(d time.Duration) *time.Time {
		t := now.Add(d)
		return &t
	}

	cases := []struct {
		name      string
		state     XIngestState
		wantStale bool
		wantRef   *time.Time
	}{
		{
			name:      "disabled push source ignored",
			state:     XIngestState{HasSource: true, HasEnabledSource: false, LastIngestAt: ptr(-48 * time.Hour)},
			wantStale: false,
		},
		{
			name:      "recent push ingest under 36h",
			state:     XIngestState{HasSource: true, HasEnabledSource: true, LastIngestAt: ptr(-35 * time.Hour)},
			wantStale: false,
			wantRef:   ptr(-35 * time.Hour),
		},
		{
			name:      "stale push ingest over 36h",
			state:     XIngestState{HasSource: true, HasEnabledSource: true, LastIngestAt: ptr(-37 * time.Hour)},
			wantStale: true,
			wantRef:   ptr(-37 * time.Hour),
		},
		{
			name:      "first day without ingest under 36h grace",
			state:     XIngestState{HasSource: true, HasEnabledSource: true, InstalledAt: ptr(-40 * time.Hour), SourceCreatedAt: ptr(-12 * time.Hour)},
			wantStale: false,
			wantRef:   ptr(-12 * time.Hour),
		},
		{
			name:      "no ingest past 36h after install and source creation",
			state:     XIngestState{HasSource: true, HasEnabledSource: true, InstalledAt: ptr(-50 * time.Hour), SourceCreatedAt: ptr(-37 * time.Hour)},
			wantStale: true,
			wantRef:   ptr(-37 * time.Hour),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stale, ref := EvaluateXIngestStale(tc.state, now, DefaultXIngestStaleAfter)
			if stale != tc.wantStale {
				t.Fatalf("stale=%v, want %v", stale, tc.wantStale)
			}
			if tc.wantRef != nil {
				if ref == nil || !ref.Equal(*tc.wantRef) {
					t.Fatalf("ref=%v, want %v", ref, tc.wantRef)
				}
			} else if ref != nil {
				t.Fatalf("ref=%v, want nil", ref)
			}
		})
	}

	// Verify CheckHealth triggers XIngestStale when EvaluateXIngestStale is true.
	al := &recordingAlerter{}
	last := now.Add(-37 * time.Hour)
	svc := &Service{
		Store:   newMemStore(),
		Alerter: al,
		XIngest: &fakeXIngestStore{state: XIngestState{HasSource: true, HasEnabledSource: true, LastIngestAt: &last}},
		Now:     func() time.Time { return now },
		Log:     slog.New(slog.DiscardHandler),
	}
	if err := svc.CheckHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(al.events) != 1 || al.events[0].kind != "x_ingest_stale" || al.events[0].lastIngest == nil || !al.events[0].lastIngest.Equal(last) {
		t.Fatalf("expected CheckHealth x_ingest_stale alert, got %+v", al.events)
	}
}
