package postgres

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/scheduler"
)

// SchedulerRepo persists scheduler watermarks in scheduler_state and provides
// session-scoped PostgreSQL advisory locks on dedicated pool connections.
type SchedulerRepo struct{ DB *pgxpool.Pool }

// TryLock tries to acquire a session-scoped Postgres advisory lock on a
// dedicated connection from the pool. When acquired is true, the caller MUST
// invoke unlock (typically via defer) to release the advisory lock on that same
// session and return the connection to the pool.
func (r SchedulerRepo) TryLock(ctx context.Context, key int64) (func(), bool, error) {
	conn, err := r.DB.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			uCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if err := conn.QueryRow(uCtx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked); err != nil || !unlocked {
				// Never return a connection still holding a session lock back into the pool.
				_ = conn.Conn().Close(uCtx)
			}
			conn.Release()
		})
	}
	return unlock, true, nil
}

// EnsureJob inserts the job anchor row if missing (preserving any existing
// installed_at across restarts) and returns the current state.
func (r SchedulerRepo) EnsureJob(ctx context.Context, job string, installedAt time.Time) (scheduler.JobState, error) {
	if installedAt.IsZero() {
		installedAt = time.Now().UTC()
	}
	if _, err := r.DB.Exec(ctx,
		`INSERT INTO scheduler_state (job_name, installed_at) VALUES ($1, $2) ON CONFLICT (job_name) DO NOTHING`,
		job, installedAt); err != nil {
		return scheduler.JobState{}, err
	}
	var st scheduler.JobState
	err := r.DB.QueryRow(ctx, `
SELECT job_name, installed_at, last_slot_at, last_run_at, last_success_at, last_status, last_error
FROM scheduler_state WHERE job_name = $1`, job).
		Scan(&st.JobName, &st.InstalledAt, &st.LastSlotAt, &st.LastRunAt, &st.LastSuccessAt, &st.LastStatus, &st.LastError)
	return st, mapErr(err)
}

// RecordRun monotonically records a scheduled job attempt and outcome.
func (r SchedulerRepo) RecordRun(ctx context.Context, job string, rec scheduler.RunRecord) error {
	runAt := rec.RunAt
	if runAt.IsZero() {
		runAt = time.Now().UTC()
	}
	_, err := r.DB.Exec(ctx, `
INSERT INTO scheduler_state (job_name, installed_at, last_slot_at, last_run_at, last_success_at, last_status, last_error, updated_at)
VALUES ($1, $2, $3, $2, $4, $5, $6, now())
ON CONFLICT (job_name) DO UPDATE SET
    last_slot_at = CASE
        WHEN EXCLUDED.last_slot_at IS NULL THEN scheduler_state.last_slot_at
        WHEN scheduler_state.last_slot_at IS NULL THEN EXCLUDED.last_slot_at
        ELSE GREATEST(scheduler_state.last_slot_at, EXCLUDED.last_slot_at)
    END,
    last_run_at = CASE
        WHEN scheduler_state.last_run_at IS NULL THEN EXCLUDED.last_run_at
        ELSE GREATEST(scheduler_state.last_run_at, EXCLUDED.last_run_at)
    END,
    last_success_at = CASE
        WHEN EXCLUDED.last_success_at IS NULL THEN scheduler_state.last_success_at
        WHEN scheduler_state.last_success_at IS NULL THEN EXCLUDED.last_success_at
        ELSE GREATEST(scheduler_state.last_success_at, EXCLUDED.last_success_at)
    END,
    last_status = EXCLUDED.last_status,
    last_error  = EXCLUDED.last_error,
    updated_at  = now()`,
		job, runAt, rec.SlotAt, rec.SuccessAt, rec.Status, clip(rec.Error))
	return err
}

// RecordManualCollect updates last_run_at for the collect job so
// COLLECT_COOLDOWN holds across restarts after a manual POST /api/pipeline/run.
func (r SchedulerRepo) RecordManualCollect(ctx context.Context, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := r.DB.Exec(ctx, `
INSERT INTO scheduler_state (job_name, installed_at, last_run_at, updated_at)
VALUES ($1, $2, $2, now())
ON CONFLICT (job_name) DO UPDATE SET
    last_run_at = CASE
        WHEN scheduler_state.last_run_at IS NULL THEN EXCLUDED.last_run_at
        ELSE GREATEST(scheduler_state.last_run_at, EXCLUDED.last_run_at)
    END,
    updated_at = now()`,
		scheduler.JobCollect, at)
	return err
}
