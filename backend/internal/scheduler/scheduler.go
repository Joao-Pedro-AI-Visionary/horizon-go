// Package scheduler runs the periodic collection (07:30 and 16:30 BRT) and
// daily 32-day retention cleanup (01:00 BRT) inside the API process, replacing
// external GitHub Actions cron schedules while coordinating across instances
// via Postgres advisory locks and persisted slot watermarks.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
	_ "time/tzdata" // guarantee America/Sao_Paulo inside minimal/distroless images

	"github.com/jpdev/horizon-go/internal/retention"
)

const (
	Timezone = retention.Timezone

	JobCollect    = "collect"
	JobCleanup    = "cleanup"
	JobNewsletter = "newsletter"

	// Distinct Postgres advisory lock keys (do not collide with dedupLock = 7_202_602).
	CollectLockID    int64 = 7_202_610
	CleanupLockID    int64 = 7_202_611
	MonitorLockID    int64 = 7_202_612
	NewsletterLockID int64 = 7_202_613

	TriggerBootCatchup = "boot_catchup"
	TriggerSchedule    = "schedule"

	DefaultCollectTimeout     = 15 * time.Minute
	DefaultCleanupTimeout     = 5 * time.Minute
	DefaultNewsletterTimeout  = 5 * time.Minute
	DefaultMissedCatchUpGrace = 3 * time.Minute
	DefaultOverdueThreshold   = 3 * time.Hour
	DefaultXIngestStaleAfter  = 36 * time.Hour
	DefaultMonitorInterval    = 15 * time.Minute
)

// TimeOfDay represents a daily wall-clock time (hour 0..23, minute 0..59) in
// the scheduler's location.
type TimeOfDay struct {
	Hour   int
	Minute int
}

// DefaultCollectSlots runs at 07:30 and 16:30 America/Sao_Paulo (10:30 and 19:30 UTC).
var DefaultCollectSlots = []TimeOfDay{
	{Hour: 7, Minute: 30},
	{Hour: 16, Minute: 30},
}

// DefaultCleanupSlots runs daily at 01:00 America/Sao_Paulo (04:00 UTC).
var DefaultCleanupSlots = []TimeOfDay{
	{Hour: 1, Minute: 0},
}

// DefaultNewsletterSlots runs daily at 08:00 America/Sao_Paulo (11:00 UTC).
var DefaultNewsletterSlots = []TimeOfDay{
	{Hour: 8, Minute: 0},
}

// JobState is the persisted state for one scheduled job.
type JobState struct {
	JobName       string
	InstalledAt   time.Time
	LastSlotAt    *time.Time
	LastRunAt     *time.Time
	LastSuccessAt *time.Time
	LastStatus    string
	LastError     string
}

// RunRecord updates scheduler_state after an attempted execution.
type RunRecord struct {
	SlotAt    *time.Time
	RunAt     time.Time
	SuccessAt *time.Time
	Status    string
	Error     string
}

// Store persists job state and provides cross-instance advisory locking.
type Store interface {
	EnsureJob(ctx context.Context, job string, installedAt time.Time) (JobState, error)
	RecordRun(ctx context.Context, job string, rec RunRecord) error
	TryLock(ctx context.Context, key int64) (unlock func(), acquired bool, err error)
}

// CollectOutcome summarizes an internal collection invocation.
type CollectOutcome struct {
	RunID      string
	Fetched    int
	Published  int
	Duplicates int
	Invalid    int
	Skipped    bool
	SkipReason string
	RetryAfter time.Duration
}

// Collector triggers an internal pipeline collection reusing the HTTP server's
// mutex and cooldown guards.
type Collector interface {
	RunScheduledCollect(ctx context.Context) (CollectOutcome, error)
}

// Cleaner runs the 32-day retention evaluation and delete.
type Cleaner interface {
	Cleanup(ctx context.Context, dryRun bool) (retention.Report, error)
}

// NewsletterOutcome summarizes a scheduled Top 5-7 newsletter dispatch.
type NewsletterOutcome struct {
	ItemCount  int
	SentCount  int
	Skipped    bool
	SkipReason string
}

// NewsletterSender dispatches the daily Top 5-7 newsletter edition.
type NewsletterSender interface {
	SendScheduled(ctx context.Context, slot time.Time) (NewsletterOutcome, error)
}

// XIngestState reports the latest X (push) ingestion timestamps from storage.
type XIngestState struct {
	HasSource        bool
	HasEnabledSource bool
	SourceCreatedAt  *time.Time
	LastIngestAt     *time.Time
	InstalledAt      *time.Time
}

// XIngestStore queries X (push) source ingestion freshness.
type XIngestStore interface {
	XIngestStatus(ctx context.Context) (XIngestState, error)
}

// Alerter dispatches operational alerts for job failures, missed/overdue slots,
// blocked cleanups, stale X ingestion, and recoveries.
type Alerter interface {
	JobFailed(ctx context.Context, job string, slot, at time.Time, err error)
	JobRecovered(ctx context.Context, job string, slot, at time.Time)
	JobMissed(ctx context.Context, job string, slot, at time.Time, viaCatchUp bool)
	CleanupBlocked(ctx context.Context, slot, at time.Time, dec retention.DeleteDecision)
	XIngestStale(ctx context.Context, lastIngestAt, anchorAt *time.Time, at time.Time)
}

// Service coordinates scheduled collection, newsletter dispatch and retention cleanup.
type Service struct {
	Store              Store
	Collector          Collector
	Cleaner            Cleaner
	Newsletter         NewsletterSender
	Alerter            Alerter
	XIngest            XIngestStore
	Timezone           string
	CollectSlots       []TimeOfDay
	CleanupSlots       []TimeOfDay
	NewsletterSlots    []TimeOfDay
	CollectCooldown    time.Duration
	CollectTimeout     time.Duration
	CleanupTimeout     time.Duration
	NewsletterTimeout  time.Duration
	MissedCatchUpGrace time.Duration // 0 uses DefaultMissedCatchUpGrace; negative disables grace
	OverdueThreshold   time.Duration
	XIngestStaleAfter  time.Duration
	MonitorInterval    time.Duration
	Now                func() time.Time
	Log                *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) location() (*time.Location, error) {
	tz := s.Timezone
	if tz == "" {
		tz = Timezone
	}
	return time.LoadLocation(tz)
}

func (s *Service) collectSlots() []TimeOfDay {
	if len(s.CollectSlots) > 0 {
		return s.CollectSlots
	}
	return DefaultCollectSlots
}

func (s *Service) cleanupSlots() []TimeOfDay {
	if len(s.CleanupSlots) > 0 {
		return s.CleanupSlots
	}
	return DefaultCleanupSlots
}

func (s *Service) newsletterSlots() []TimeOfDay {
	if len(s.NewsletterSlots) > 0 {
		return s.NewsletterSlots
	}
	return DefaultNewsletterSlots
}

func (s *Service) newsletterTimeout() time.Duration {
	if s.NewsletterTimeout > 0 {
		return s.NewsletterTimeout
	}
	return DefaultNewsletterTimeout
}

func (s *Service) collectTimeout() time.Duration {
	if s.CollectTimeout > 0 {
		return s.CollectTimeout
	}
	return DefaultCollectTimeout
}

func (s *Service) cleanupTimeout() time.Duration {
	if s.CleanupTimeout > 0 {
		return s.CleanupTimeout
	}
	return DefaultCleanupTimeout
}

func (s *Service) missedCatchUpGrace() time.Duration {
	if s.MissedCatchUpGrace < 0 {
		return 0
	}
	if s.MissedCatchUpGrace > 0 {
		return s.MissedCatchUpGrace
	}
	return DefaultMissedCatchUpGrace
}

func (s *Service) overdueThreshold() time.Duration {
	if s.OverdueThreshold > 0 {
		return s.OverdueThreshold
	}
	return DefaultOverdueThreshold
}

func (s *Service) xIngestStaleAfter() time.Duration {
	if s.XIngestStaleAfter > 0 {
		return s.XIngestStaleAfter
	}
	return DefaultXIngestStaleAfter
}

func (s *Service) monitorInterval() time.Duration {
	if s.MonitorInterval > 0 {
		return s.MonitorInterval
	}
	return DefaultMonitorInterval
}

// LatestSlot returns the most recent scheduled slot at or before now in loc.
func LatestSlot(now time.Time, loc *time.Location, slots []TimeOfDay) time.Time {
	if len(slots) == 0 {
		return time.Time{}
	}
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	yesterday := today.AddDate(0, 0, -1)

	var best time.Time
	for _, day := range []time.Time{yesterday, today} {
		for _, t := range slots {
			cand := time.Date(day.Year(), day.Month(), day.Day(), t.Hour, t.Minute, 0, 0, loc)
			if !cand.After(local) && (best.IsZero() || cand.After(best)) {
				best = cand
			}
		}
	}
	return best
}

// NextSlot returns the earliest scheduled slot strictly after after in loc.
func NextSlot(after time.Time, loc *time.Location, slots []TimeOfDay) time.Time {
	if len(slots) == 0 {
		return time.Time{}
	}
	local := after.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	tomorrow := today.AddDate(0, 0, 1)

	var best time.Time
	for _, day := range []time.Time{today, tomorrow} {
		for _, t := range slots {
			cand := time.Date(day.Year(), day.Month(), day.Day(), t.Hour, t.Minute, 0, 0, loc)
			if cand.After(local) && (best.IsZero() || cand.Before(best)) {
				best = cand
			}
		}
	}
	return best
}

type scheduledEvent struct {
	Job  string
	Slot time.Time
}

func (s *Service) nextEvent(after time.Time, loc *time.Location) (scheduledEvent, bool) {
	var events []scheduledEvent
	if s.Cleaner != nil {
		if t := NextSlot(after, loc, s.cleanupSlots()); !t.IsZero() {
			events = append(events, scheduledEvent{Job: JobCleanup, Slot: t})
		}
	}
	if s.Collector != nil {
		if t := NextSlot(after, loc, s.collectSlots()); !t.IsZero() {
			events = append(events, scheduledEvent{Job: JobCollect, Slot: t})
		}
	}
	if s.Newsletter != nil {
		if t := NextSlot(after, loc, s.newsletterSlots()); !t.IsZero() {
			events = append(events, scheduledEvent{Job: JobNewsletter, Slot: t})
		}
	}
	if len(events) == 0 {
		return scheduledEvent{}, false
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Slot.Equal(events[j].Slot) {
			// Never run simultaneously on the same instant: cleanup -> collect -> newsletter.
			order := map[string]int{JobCleanup: 0, JobCollect: 1, JobNewsletter: 2}
			return order[events[i].Job] < order[events[j].Job]
		}
		return events[i].Slot.Before(events[j].Slot)
	})
	return events[0], true
}

// CatchUp checks the latest due slot for cleanup and collect sequentially and
// executes any missed slot that satisfies safety guards and cooldowns.
func (s *Service) CatchUp(ctx context.Context) error {
	loc, err := s.location()
	if err != nil {
		return err
	}
	var errs []error
	if s.Cleaner != nil {
		if slot := LatestSlot(s.now(), loc, s.cleanupSlots()); !slot.IsZero() {
			if err := s.runCleanupSlot(ctx, slot, TriggerBootCatchup); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
		return errors.Join(errs...)
	}
	if s.Collector != nil {
		if slot := LatestSlot(s.now(), loc, s.collectSlots()); !slot.IsZero() {
			if err := s.runCollectSlot(ctx, slot, TriggerBootCatchup); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
		return errors.Join(errs...)
	}
	if s.Newsletter != nil {
		if slot := LatestSlot(s.now(), loc, s.newsletterSlots()); !slot.IsZero() {
			if err := s.runNewsletterSlot(ctx, slot, TriggerBootCatchup); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Run initializes job anchors, runs boot catch-up, and then executes scheduled
// slots sequentially until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	loc, err := s.location()
	if err != nil {
		return err
	}
	bootAt := s.now()
	if s.Cleaner != nil {
		if _, err := s.Store.EnsureJob(ctx, JobCleanup, bootAt); err != nil {
			s.log().Error("scheduler init failed", "job", JobCleanup, "err", err)
		}
	}
	if s.Collector != nil {
		if _, err := s.Store.EnsureJob(ctx, JobCollect, bootAt); err != nil {
			s.log().Error("scheduler init failed", "job", JobCollect, "err", err)
		}
	}
	if s.Newsletter != nil {
		if _, err := s.Store.EnsureJob(ctx, JobNewsletter, bootAt); err != nil {
			s.log().Error("scheduler init failed", "job", JobNewsletter, "err", err)
		}
	}
	if err := s.CatchUp(ctx); err != nil && ctx.Err() == nil {
		s.log().Error("scheduler boot catch-up finished with errors", "err", err)
	}
	if err := s.CheckHealth(ctx); err != nil && ctx.Err() == nil {
		s.log().Warn("scheduler boot health check failed", "err", err)
	}

	// Start the future schedule cursor at bootAt so a boundary crossed while
	// CatchUp was running is never skipped.
	lastSlot := bootAt
	for {
		if ctx.Err() != nil {
			return nil
		}
		cursor := s.now()
		if lastSlot.Equal(bootAt) {
			cursor = bootAt
		} else if !cursor.After(lastSlot) {
			cursor = lastSlot.Add(time.Second)
		}
		ev, ok := s.nextEvent(cursor, loc)
		if !ok {
			<-ctx.Done()
			return nil
		}
		s.log().Info("scheduler waiting", "next_job", ev.Job, "next_slot", ev.Slot.In(loc).Format(time.RFC3339))

		for {
			wait := ev.Slot.Sub(s.now())
			if wait <= 0 {
				break
			}
			interval := s.monitorInterval()
			sleep := wait
			checkAfter := false
			if s.Alerter != nil && interval > 0 && sleep > interval {
				sleep = interval
				checkAfter = true
			}
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			if checkAfter {
				if err := s.CheckHealth(ctx); err != nil && ctx.Err() == nil {
					s.log().Warn("scheduler health check failed", "err", err)
				}
			}
		}

		var runErr error
		switch ev.Job {
		case JobCleanup:
			runErr = s.runCleanupSlot(ctx, ev.Slot, TriggerSchedule)
		case JobCollect:
			runErr = s.runCollectSlot(ctx, ev.Slot, TriggerSchedule)
		case JobNewsletter:
			runErr = s.runNewsletterSlot(ctx, ev.Slot, TriggerSchedule)
		}
		if runErr != nil && ctx.Err() == nil {
			s.log().Error("scheduled job failed", "job", ev.Job, "slot", ev.Slot.In(loc).Format(time.RFC3339), "err", runErr)
		}
		lastSlot = ev.Slot
	}
}

func (s *Service) runCollectSlot(ctx context.Context, slot time.Time, trigger string) error {
	start := s.now()
	log := s.log().With("job", JobCollect, "trigger", trigger, "slot", slot.Format(time.RFC3339))
	log.Info("scheduler job start")

	unlock, acquired, err := s.Store.TryLock(ctx, CollectLockID)
	if err != nil {
		log.Error("scheduler job error", "stage", "advisory_lock", "duration", s.now().Sub(start).Round(time.Millisecond), "err", err)
		return fmt.Errorf("collect advisory lock: %w", err)
	}
	if !acquired {
		log.Info("scheduler job skipped", "reason", "advisory_lock_held", "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}
	defer unlock()

	state, err := s.Store.EnsureJob(ctx, JobCollect, start)
	if err != nil {
		log.Error("scheduler job error", "stage", "load_state", "duration", s.now().Sub(start).Round(time.Millisecond), "err", err)
		return fmt.Errorf("collect load state: %w", err)
	}
	if state.LastSlotAt != nil && !state.LastSlotAt.Before(slot) {
		log.Info("scheduler job skipped", "reason", "slot_already_completed", "last_slot", state.LastSlotAt.Format(time.RFC3339), "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}
	if s.CollectCooldown > 0 && state.LastRunAt != nil {
		elapsed := s.now().Sub(*state.LastRunAt)
		if elapsed < 0 {
			elapsed = 0
		}
		if wait := s.CollectCooldown - elapsed; wait > 0 {
			log.Info("scheduler job skipped", "reason", "cooldown", "retry_after", wait.Round(time.Second), "duration", s.now().Sub(start).Round(time.Millisecond))
			return nil
		}
	}

	jctx, cancel := context.WithTimeout(ctx, s.collectTimeout())
	defer cancel()

	if s.Alerter != nil && slot.After(state.InstalledAt) {
		delay := start.Sub(slot)
		if (trigger == TriggerBootCatchup && delay > s.missedCatchUpGrace()) || delay > s.overdueThreshold() {
			s.Alerter.JobMissed(ctx, JobCollect, slot, start, trigger == TriggerBootCatchup)
		}
	}

	out, runErr := s.Collector.RunScheduledCollect(jctx)
	finished := s.now()
	dur := finished.Sub(start).Round(time.Millisecond)

	if out.Skipped {
		log.Info("scheduler job skipped", "reason", out.SkipReason, "retry_after", out.RetryAfter.Round(time.Second), "duration", dur)
		return nil
	}
	if runErr != nil {
		recErr := s.Store.RecordRun(context.WithoutCancel(ctx), JobCollect, RunRecord{
			RunAt:  start,
			Status: "error",
			Error:  runErr.Error(),
		})
		log.Error("scheduler job error", "run_id", out.RunID, "fetched", out.Fetched, "published", out.Published, "duration", dur, "err", runErr)
		if s.Alerter != nil && ctx.Err() == nil {
			s.Alerter.JobFailed(ctx, JobCollect, slot, finished, runErr)
		}
		return errors.Join(runErr, recErr)
	}
	prevFailed := state.LastStatus == "error"
	if err := s.Store.RecordRun(context.WithoutCancel(ctx), JobCollect, RunRecord{
		SlotAt:    &slot,
		RunAt:     start,
		SuccessAt: &finished,
		Status:    "ok",
	}); err != nil {
		log.Error("scheduler state persist failed", "run_id", out.RunID, "duration", dur, "err", err)
		return err
	}
	if prevFailed && s.Alerter != nil {
		s.Alerter.JobRecovered(ctx, JobCollect, slot, finished)
	}
	log.Info("scheduler job done", "run_id", out.RunID, "fetched", out.Fetched, "published", out.Published, "duplicates", out.Duplicates, "invalid", out.Invalid, "duration", dur)
	return nil
}

func (s *Service) runCleanupSlot(ctx context.Context, slot time.Time, trigger string) error {
	start := s.now()
	log := s.log().With("job", JobCleanup, "trigger", trigger, "slot", slot.Format(time.RFC3339))
	log.Info("scheduler job start")

	unlock, acquired, err := s.Store.TryLock(ctx, CleanupLockID)
	if err != nil {
		log.Error("scheduler job error", "stage", "advisory_lock", "duration", s.now().Sub(start).Round(time.Millisecond), "err", err)
		return fmt.Errorf("cleanup advisory lock: %w", err)
	}
	if !acquired {
		log.Info("scheduler job skipped", "reason", "advisory_lock_held", "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}
	defer unlock()

	state, err := s.Store.EnsureJob(ctx, JobCleanup, start)
	if err != nil {
		log.Error("scheduler job error", "stage", "load_state", "duration", s.now().Sub(start).Round(time.Millisecond), "err", err)
		return fmt.Errorf("cleanup load state: %w", err)
	}
	if !slot.After(state.InstalledAt) {
		log.Info("scheduler job skipped", "reason", "slot_before_install", "installed_at", state.InstalledAt.Format(time.RFC3339), "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}
	if state.LastSlotAt != nil && !state.LastSlotAt.Before(slot) {
		log.Info("scheduler job skipped", "reason", "slot_already_completed", "last_slot", state.LastSlotAt.Format(time.RFC3339), "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}

	jctx, cancel := context.WithTimeout(ctx, s.cleanupTimeout())
	defer cancel()

	if s.Alerter != nil && slot.After(state.InstalledAt) {
		delay := start.Sub(slot)
		if (trigger == TriggerBootCatchup && delay > s.missedCatchUpGrace()) || delay > s.overdueThreshold() {
			s.Alerter.JobMissed(ctx, JobCleanup, slot, start, trigger == TriggerBootCatchup)
		}
	}

	dry, err := s.Cleaner.Cleanup(jctx, true)
	if err != nil {
		finished := s.now()
		dur := finished.Sub(start).Round(time.Millisecond)
		recErr := s.Store.RecordRun(context.WithoutCancel(ctx), JobCleanup, RunRecord{
			RunAt:  start,
			Status: "error",
			Error:  err.Error(),
		})
		log.Error("scheduler job error", "stage", "dry_run", "duration", dur, "err", err)
		if s.Alerter != nil && ctx.Err() == nil {
			s.Alerter.JobFailed(ctx, JobCleanup, slot, finished, err)
		}
		return errors.Join(err, recErr)
	}

	prevFailed := state.LastStatus == "error"
	dec := retention.CanDelete(dry.Counts)
	if !dec.Allow {
		finished := s.now()
		dur := finished.Sub(start).Round(time.Millisecond)
		if dec.Items <= 0 {
			if recErr := s.Store.RecordRun(context.WithoutCancel(ctx), JobCleanup, RunRecord{
				SlotAt:    &slot,
				RunAt:     start,
				SuccessAt: &finished,
				Status:    "skipped",
				Error:     dec.Reason,
			}); recErr != nil {
				log.Error("scheduler state persist failed", "duration", dur, "err", recErr)
				return recErr
			}
			log.Info("scheduler job skipped", "reason", dec.Reason, "dry_run", true, "cutoff", dry.Cutoff, "counts", dry.Counts, "duration", dur)
			if prevFailed && s.Alerter != nil {
				s.Alerter.JobRecovered(ctx, JobCleanup, slot, finished)
			}
			return nil
		}
		if recErr := s.Store.RecordRun(context.WithoutCancel(ctx), JobCleanup, RunRecord{
			SlotAt: &slot,
			RunAt:  start,
			Status: "blocked",
			Error:  dec.Reason,
		}); recErr != nil {
			log.Error("scheduler state persist failed", "duration", dur, "err", recErr)
			return recErr
		}
		log.Warn("scheduler job skipped", "reason", dec.Reason, "blocked", true, "dry_run", true, "cutoff", dry.Cutoff, "counts", dry.Counts, "duration", dur)
		if s.Alerter != nil {
			s.Alerter.CleanupBlocked(ctx, slot, finished, dec)
		}
		return nil
	}

	realRep, err := s.Cleaner.Cleanup(jctx, false)
	finished := s.now()
	dur := finished.Sub(start).Round(time.Millisecond)
	if err != nil {
		recErr := s.Store.RecordRun(context.WithoutCancel(ctx), JobCleanup, RunRecord{
			RunAt:  start,
			Status: "error",
			Error:  err.Error(),
		})
		log.Error("scheduler job error", "stage", "delete", "dry_counts", dry.Counts, "duration", dur, "err", err)
		if s.Alerter != nil && ctx.Err() == nil {
			s.Alerter.JobFailed(ctx, JobCleanup, slot, finished, err)
		}
		return errors.Join(err, recErr)
	}
	if err := s.Store.RecordRun(context.WithoutCancel(ctx), JobCleanup, RunRecord{
		SlotAt:    &slot,
		RunAt:     start,
		SuccessAt: &finished,
		Status:    "ok",
	}); err != nil {
		log.Error("scheduler state persist failed", "duration", dur, "err", err)
		return err
	}
	if prevFailed && s.Alerter != nil {
		s.Alerter.JobRecovered(ctx, JobCleanup, slot, finished)
	}
	log.Info("scheduler job done", "dry_run", false, "cutoff", realRep.Cutoff, "dry_counts", dry.Counts, "deleted_counts", realRep.Counts, "duration", dur)
	return nil
}

func (s *Service) runNewsletterSlot(ctx context.Context, slot time.Time, trigger string) error {
	start := s.now()
	log := s.log().With("job", JobNewsletter, "trigger", trigger, "slot", slot.Format(time.RFC3339))
	log.Info("scheduler job start")

	unlock, acquired, err := s.Store.TryLock(ctx, NewsletterLockID)
	if err != nil {
		log.Error("scheduler job error", "stage", "advisory_lock", "duration", s.now().Sub(start).Round(time.Millisecond), "err", err)
		return fmt.Errorf("newsletter advisory lock: %w", err)
	}
	if !acquired {
		log.Info("scheduler job skipped", "reason", "advisory_lock_held", "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}
	defer unlock()

	state, err := s.Store.EnsureJob(ctx, JobNewsletter, start)
	if err != nil {
		log.Error("scheduler job error", "stage", "load_state", "duration", s.now().Sub(start).Round(time.Millisecond), "err", err)
		return fmt.Errorf("newsletter load state: %w", err)
	}
	if !slot.After(state.InstalledAt) {
		log.Info("scheduler job skipped", "reason", "slot_before_install", "installed_at", state.InstalledAt.Format(time.RFC3339), "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}
	if state.LastSlotAt != nil && !state.LastSlotAt.Before(slot) {
		log.Info("scheduler job skipped", "reason", "slot_already_completed", "last_slot", state.LastSlotAt.Format(time.RFC3339), "duration", s.now().Sub(start).Round(time.Millisecond))
		return nil
	}

	jctx, cancel := context.WithTimeout(ctx, s.newsletterTimeout())
	defer cancel()

	if s.Alerter != nil && slot.After(state.InstalledAt) {
		delay := start.Sub(slot)
		if (trigger == TriggerBootCatchup && delay > s.missedCatchUpGrace()) || delay > s.overdueThreshold() {
			s.Alerter.JobMissed(ctx, JobNewsletter, slot, start, trigger == TriggerBootCatchup)
		}
	}

	out, runErr := s.Newsletter.SendScheduled(jctx, slot)
	finished := s.now()
	dur := finished.Sub(start).Round(time.Millisecond)
	if runErr != nil {
		recErr := s.Store.RecordRun(context.WithoutCancel(ctx), JobNewsletter, RunRecord{
			RunAt:  start,
			Status: "error",
			Error:  runErr.Error(),
		})
		log.Error("scheduler job error", "duration", dur, "err", runErr)
		if s.Alerter != nil && ctx.Err() == nil {
			s.Alerter.JobFailed(ctx, JobNewsletter, slot, finished, runErr)
		}
		return errors.Join(runErr, recErr)
	}
	prevFailed := state.LastStatus == "error"
	status := "ok"
	if out.Skipped {
		status = "skipped"
	}
	if err := s.Store.RecordRun(context.WithoutCancel(ctx), JobNewsletter, RunRecord{
		SlotAt:    &slot,
		RunAt:     start,
		SuccessAt: &finished,
		Status:    status,
		Error:     out.SkipReason,
	}); err != nil {
		log.Error("scheduler state persist failed", "duration", dur, "err", err)
		return err
	}
	if prevFailed && s.Alerter != nil {
		s.Alerter.JobRecovered(ctx, JobNewsletter, slot, finished)
	}
	log.Info("scheduler job done", "items", out.ItemCount, "sent", out.SentCount, "skipped", out.Skipped, "reason", out.SkipReason, "duration", dur)
	return nil
}

// EvaluateXIngestStale returns whether the X (push) source is stale (> threshold
// since last item, or > threshold since installation/source creation when no
// items have been pushed yet) together with the relevant reference timestamp.
func EvaluateXIngestStale(st XIngestState, now time.Time, threshold time.Duration) (stale bool, ref *time.Time) {
	if st.HasSource && !st.HasEnabledSource {
		return false, nil
	}
	if st.LastIngestAt != nil {
		if now.Sub(*st.LastIngestAt) > threshold {
			t := *st.LastIngestAt
			return true, &t
		}
		return false, st.LastIngestAt
	}
	var anchor *time.Time
	if st.InstalledAt != nil {
		t := *st.InstalledAt
		anchor = &t
	}
	if st.HasEnabledSource && st.SourceCreatedAt != nil {
		if anchor == nil || st.SourceCreatedAt.After(*anchor) {
			t := *st.SourceCreatedAt
			anchor = &t
		}
	}
	if anchor != nil && now.Sub(*anchor) > threshold {
		return true, anchor
	}
	return false, anchor
}

// CheckHealth checks for scheduled jobs overdue by more than OverdueThreshold
// (3h) and for stale X (push) ingestion (> 36h), guarded by an advisory lock.
func (s *Service) CheckHealth(ctx context.Context) error {
	if s.Alerter == nil {
		return nil
	}
	loc, err := s.location()
	if err != nil {
		return err
	}
	unlock, acquired, err := s.Store.TryLock(ctx, MonitorLockID)
	if err != nil {
		return fmt.Errorf("monitor advisory lock: %w", err)
	}
	if !acquired {
		return nil
	}
	defer unlock()

	now := s.now()
	overdue := s.overdueThreshold()
	checks := []struct {
		enabled bool
		job     string
		slots   []TimeOfDay
	}{
		{s.Cleaner != nil, JobCleanup, s.cleanupSlots()},
		{s.Collector != nil, JobCollect, s.collectSlots()},
		{s.Newsletter != nil, JobNewsletter, s.newsletterSlots()},
	}
	for _, c := range checks {
		if !c.enabled {
			continue
		}
		slot := LatestSlot(now, loc, c.slots)
		if slot.IsZero() {
			continue
		}
		st, err := s.Store.EnsureJob(ctx, c.job, now)
		if err != nil {
			return fmt.Errorf("monitor load %s state: %w", c.job, err)
		}
		if !slot.After(st.InstalledAt) {
			continue
		}
		if st.LastSlotAt != nil && !st.LastSlotAt.Before(slot) {
			continue
		}
		if now.Sub(slot) > overdue {
			s.Alerter.JobMissed(ctx, c.job, slot, now, false)
		}
	}

	if s.XIngest != nil {
		xst, err := s.XIngest.XIngestStatus(ctx)
		if err != nil {
			return fmt.Errorf("monitor x ingest: %w", err)
		}
		if stale, ref := EvaluateXIngestStale(xst, now, s.xIngestStaleAfter()); stale {
			s.Alerter.XIngestStale(ctx, xst.LastIngestAt, ref, now)
		}
	}
	return nil
}
