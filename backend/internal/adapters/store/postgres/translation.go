package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/jpdev/horizon-go/internal/domain"
)

// SaveTranslation stores the pt-BR copy of an item once and mirrors it into
// feed_view when the item is already projected. It never touches items.title
// or items.summary. It returns false when the item already had a title_pt
// (another worker or the backfill won) or does not exist.
func (r AIRepo) SaveTranslation(ctx context.Context, id int64, t domain.Translation) (bool, error) {
	if t.TitlePT == "" {
		return false, errors.New("save translation: empty title_pt")
	}
	saved := false
	err := pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
UPDATE items SET title_pt = $2, summary_pt = $3, pt_source = $4, translated_at = now(), updated_at = now()
WHERE id = $1 AND title_pt IS NULL`, id, t.TitlePT, t.SummaryPT, t.Source())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		saved = true
		_, err = tx.Exec(ctx, `UPDATE feed_view SET title_pt = $2, summary_pt = $3 WHERE item_id = $1`, id, t.TitlePT, t.SummaryPT)
		return err
	})
	if err != nil {
		return false, err
	}
	return saved, nil
}
