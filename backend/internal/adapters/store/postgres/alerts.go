package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/scheduler"
)

// maxAlertMessage caps stored and returned messages.
const maxAlertMessage = 500

func clip(s string) string {
	if len(s) <= maxAlertMessage {
		return s
	}
	cut := maxAlertMessage
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// RecordFailure stores a failure that leaves no other trace (digest
// generation). It is read back by Alerts.
func (r ReadRepo) RecordFailure(ctx context.Context, kind, name, message string) error {
	_, err := r.DB.Exec(ctx, `INSERT INTO alert_events (kind, name, message) VALUES ($1, $2, $3)`, kind, name, clip(message))
	return err
}

// RecordAlertEvent stores an alert event with an explicit timestamp so the
// admin alerts badge (GET /api/admin/alerts) surfaces operational failures,
// missed slots, blocked cleanups, X ingest staleness, and recoveries.
func (r ReadRepo) RecordAlertEvent(ctx context.Context, kind, name, message string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := r.DB.Exec(ctx, `INSERT INTO alert_events (kind, name, message, at) VALUES ($1, $2, $3, $4)`,
		kind, name, clip(message), at.UTC())
	return err
}

// HasRecentAlertEvent reports whether an open event for (kind, name) was
// already recorded at or after since.
func (r ReadRepo) HasRecentAlertEvent(ctx context.Context, kind, name string, since time.Time) (bool, error) {
	var ok bool
	err := r.DB.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM alert_events e
    WHERE e.kind = $1 AND e.name = $2 AND e.at >= $3
      AND NOT (e.kind = 'x_ingest_stale' AND EXISTS (
          SELECT 1 FROM items i JOIN sources s ON s.id = i.source_id
          WHERE s.kind = 'push' AND i.fetched_at > e.at
      ))
)`, kind, name, since.UTC()).Scan(&ok)
	return ok, err
}

// ClaimEmailCooldown atomically claims the right to send an operational alert
// e-mail for kind when no e-mail of that kind was sent within cooldown. Using a
// short row-locked transaction prevents concurrent instances from piercing the
// cooldown without holding a DB lock across the Resend HTTP request.
func (r ReadRepo) ClaimEmailCooldown(ctx context.Context, kind string, now time.Time, cooldown time.Duration) (*time.Time, bool, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
INSERT INTO alert_cooldowns (kind, last_sent_at)
VALUES ($1, '1970-01-01T00:00:00Z'::timestamptz)
ON CONFLICT (kind) DO NOTHING`, kind); err != nil {
		return nil, false, err
	}

	var prev time.Time
	if err := tx.QueryRow(ctx, `SELECT last_sent_at FROM alert_cooldowns WHERE kind = $1 FOR UPDATE`, kind).Scan(&prev); err != nil {
		return nil, false, err
	}
	prev = prev.UTC()
	var prevPtr *time.Time
	if prev.Year() > 1970 {
		cp := prev
		prevPtr = &cp
	}
	if prevPtr != nil && prev.After(now.Add(-cooldown)) {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return prevPtr, false, nil
	}

	if _, err := tx.Exec(ctx, `UPDATE alert_cooldowns SET last_sent_at = $2, updated_at = now() WHERE kind = $1`, kind, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return prevPtr, true, nil
}

// ReleaseEmailCooldown reverts ClaimEmailCooldown if sending the e-mail failed,
// only when the stored timestamp still matches claimedAt.
func (r ReadRepo) ReleaseEmailCooldown(ctx context.Context, kind string, claimedAt time.Time, prev *time.Time) error {
	claimedAt = claimedAt.UTC()
	if prev == nil {
		_, err := r.DB.Exec(ctx, `DELETE FROM alert_cooldowns WHERE kind = $1 AND last_sent_at = $2`, kind, claimedAt)
		return err
	}
	_, err := r.DB.Exec(ctx, `
UPDATE alert_cooldowns SET last_sent_at = $2, updated_at = now()
WHERE kind = $1 AND last_sent_at = $3`, kind, prev.UTC(), claimedAt)
	return err
}

// XIngestStatus inspects existing sources, items and scheduler_state tables to
// determine when the last X (push) item was ingested and the installation anchor.
func (r ReadRepo) XIngestStatus(ctx context.Context) (scheduler.XIngestState, error) {
	var st scheduler.XIngestState
	err := r.DB.QueryRow(ctx, `
SELECT
    EXISTS(SELECT 1 FROM sources WHERE kind = $1),
    EXISTS(SELECT 1 FROM sources WHERE kind = $1 AND enabled),
    (SELECT min(created_at) FROM sources WHERE kind = $1 AND enabled),
    (SELECT max(i.fetched_at)
     FROM items i
     JOIN sources s ON s.id = i.source_id
     WHERE s.kind = $1 AND s.enabled),
    (SELECT min(installed_at) FROM scheduler_state)
`, domain.KindPush).Scan(&st.HasSource, &st.HasEnabledSource, &st.SourceCreatedAt, &st.LastIngestAt, &st.InstalledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	return st, err
}

// Alerts lists the failures still open since `since`, one per source (or
// name) with its latest error:
//   - enabled sources whose most recent fetch in the window failed (a later
//     successful fetch closes the alert);
//   - sources with items left unscored after a model error (still queued or
//     failed for good);
//   - recorded digest failures not followed by a saved digest;
//   - no successful fetch at all for `stale` (when stale > 0).
func (r ReadRepo) Alerts(ctx context.Context, since, now time.Time, stale time.Duration) ([]domain.Alert, error) {
	out := []domain.Alert{}

	rows, err := r.DB.Query(ctx, `
SELECT source_id, name, error, finished_at FROM (
    SELECT DISTINCT ON (f.source_id) f.source_id, s.name, f.status, f.error, f.finished_at
    FROM fetch_runs f JOIN sources s ON s.id = f.source_id
    WHERE f.started_at >= $1 AND s.enabled
    ORDER BY f.source_id, f.started_at DESC, f.id DESC
) last WHERE status = 'error'`, since)
	if err != nil {
		return nil, fmt.Errorf("source alerts: %w", err)
	}
	for rows.Next() {
		var a domain.Alert
		var id int64
		if err := rows.Scan(&id, &a.Name, &a.Message, &a.At); err != nil {
			rows.Close()
			return nil, err
		}
		a.Kind, a.SourceID, a.Message = domain.AlertSource, &id, clip(a.Message)
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.DB.Query(ctx, `
SELECT DISTINCT ON (i.source_id) i.source_id, s.name, i.last_error, i.updated_at,
       count(*) OVER (PARTITION BY i.source_id)
FROM items i JOIN sources s ON s.id = i.source_id
WHERE i.status IN ('raw', 'failed') AND i.last_error <> '' AND i.updated_at >= $1
ORDER BY i.source_id, i.updated_at DESC, i.id DESC`, since)
	if err != nil {
		return nil, fmt.Errorf("scoring alerts: %w", err)
	}
	for rows.Next() {
		var a domain.Alert
		var id int64
		var n int
		var msg string
		if err := rows.Scan(&id, &a.Name, &msg, &a.At, &n); err != nil {
			rows.Close()
			return nil, err
		}
		word := "itens"
		if n == 1 {
			word = "item"
		}
		a.Kind, a.SourceID, a.Message = domain.AlertScoring, &id, clip(fmt.Sprintf("%d %s sem nota: %s", n, word, msg))
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.DB.Query(ctx, `
SELECT DISTINCT ON (e.kind, e.name) e.kind, e.name, e.message, e.at
FROM alert_events e
WHERE e.at >= $1
  AND NOT (e.kind = 'digest' AND EXISTS (SELECT 1 FROM digests d WHERE d.updated_at > e.at))
  AND NOT (e.kind = 'job_failed' AND EXISTS (
      SELECT 1 FROM alert_events r WHERE r.kind = 'job_recovered' AND r.name = e.name AND r.at > e.at
  ))
  AND NOT (e.kind = 'job_recovered' AND EXISTS (
      SELECT 1 FROM alert_events f WHERE f.kind = 'job_failed' AND f.name = e.name AND f.at > e.at
  ))
  AND NOT (e.kind = 'cleanup_blocked' AND EXISTS (
      SELECT 1 FROM scheduler_state ss WHERE ss.job_name = 'cleanup' AND ss.last_status = 'ok' AND ss.last_success_at > e.at
  ))
  AND NOT (e.kind = 'x_ingest_stale' AND EXISTS (
      SELECT 1 FROM items i JOIN sources s ON s.id = i.source_id WHERE s.kind = 'push' AND i.fetched_at > e.at
  ))
ORDER BY e.kind, e.name, e.at DESC, e.id DESC`, since)
	if err != nil {
		return nil, fmt.Errorf("event alerts: %w", err)
	}
	for rows.Next() {
		var a domain.Alert
		if err := rows.Scan(&a.Kind, &a.Name, &a.Message, &a.At); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if stale > 0 {
		var last *time.Time
		if err := r.DB.QueryRow(ctx, `SELECT max(finished_at) FROM fetch_runs WHERE status = 'ok'`).Scan(&last); err != nil {
			return nil, fmt.Errorf("last collection: %w", err)
		}
		if last == nil || now.Sub(*last) > stale {
			a := domain.Alert{Kind: domain.AlertCollection, Name: "Coleta", At: now}
			h := int(stale.Round(time.Hour) / time.Hour)
			if last == nil {
				a.Message = "Nenhuma coleta concluída com sucesso ainda."
			} else {
				a.At = *last
				a.Message = fmt.Sprintf("Nenhuma coleta com sucesso nas últimas %d h.", h)
			}
			out = append(out, a)
		}
	}
	return out, nil
}
