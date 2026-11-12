package postgres

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jpdev/horizon-go/internal/domain"
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
