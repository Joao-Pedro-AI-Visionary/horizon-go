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
