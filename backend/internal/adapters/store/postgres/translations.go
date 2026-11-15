package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/jpdev/horizon-go/internal/domain"
)

// translatableStatuses are the processed items that get a pt-BR copy
// (the same set as items_pending_translation_idx, migration 00015).
const translatableStatuses = `('scored', 'deduped', 'duplicate')`

// TranslateRow is an item waiting for its pt-BR copy.
type TranslateRow struct {
	ID       int64
	Title    string
	Summary  string
	Attempts int
}

// PendingTranslations lists processed items without title_pt that still have
// attempts left, oldest id first. Items that already have title_pt are never
// returned, so a sweep never retranslates.
func (r AIRepo) PendingTranslations(ctx context.Context, maxAttempts, limit int) ([]TranslateRow, error) {
	rows, err := r.DB.Query(ctx, `
SELECT id, title, COALESCE(summary, ''), translate_attempts
FROM items
WHERE title_pt IS NULL AND status IN `+translatableStatuses+` AND translate_attempts < $1
ORDER BY id LIMIT $2`, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TranslateRow, error) {
		var t TranslateRow
		err := row.Scan(&t.ID, &t.Title, &t.Summary, &t.Attempts)
		return t, err
	})
}

// BumpTranslateAttempts spends one translation attempt of an item still
// without title_pt. At the maximum the item leaves PendingTranslations
// (it stays counted as failed in TranslationStatus).
func (r AIRepo) BumpTranslateAttempts(ctx context.Context, id int64) error {
	_, err := r.DB.Exec(ctx, `
UPDATE items SET translate_attempts = translate_attempts + 1
WHERE id = $1 AND title_pt IS NULL`, id)
	return err
}

// TranslationStatus counts the backfill state of the processed items.
func (r AIRepo) TranslationStatus(ctx context.Context, maxAttempts int) (domain.TranslationStatus, error) {
	var s domain.TranslationStatus
	err := r.DB.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE title_pt IS NULL AND translate_attempts < $1),
       count(*) FILTER (WHERE title_pt IS NOT NULL AND pt_source = 'llm'),
       count(*) FILTER (WHERE title_pt IS NOT NULL AND pt_source = 'copy'),
       count(*) FILTER (WHERE title_pt IS NULL AND translate_attempts >= $1)
FROM items WHERE status IN `+translatableStatuses, maxAttempts).
		Scan(&s.Remaining, &s.Translated, &s.Copied, &s.Failed)
	return s, err
}
