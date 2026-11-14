package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/newsletter"
)

// NewsletterRepo implements newsletter.Store over PostgreSQL.
type NewsletterRepo struct{ DB *pgxpool.Pool }

// GetSettings delegates to ReadRepo to avoid duplicating settings SQL.
func (r NewsletterRepo) GetSettings(ctx context.Context) (domain.Settings, error) {
	return ReadRepo{DB: r.DB}.GetSettings(ctx)
}

// Subscribe inserts a pending subscriber or refreshes confirmation details unless already active.
func (r NewsletterRepo) Subscribe(ctx context.Context, email, confirmHash, unsubToken string, expiresAt, now time.Time) (newsletter.Subscriber, bool, error) {
	var sub newsletter.Subscriber
	var alreadyActive bool
	err := pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		var curStatus, curUnsub string
		var curID int64
		err := tx.QueryRow(ctx, `SELECT id, status, unsub_token FROM newsletter_subscribers WHERE email = $1 FOR UPDATE`, email).
			Scan(&curID, &curStatus, &curUnsub)
		if err == nil && curStatus == newsletter.StatusActive {
			alreadyActive = true
			sub = newsletter.Subscriber{ID: curID, Email: email, Status: curStatus, UnsubToken: curUnsub}
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(ctx, `
INSERT INTO newsletter_subscribers (email, status, confirm_token_hash, confirm_expires_at, unsub_token, created_at, updated_at)
VALUES ($1, 'pending', $2, $3, $4, $5, $5)
ON CONFLICT (email) DO UPDATE SET
	status = 'pending',
	confirm_token_hash = EXCLUDED.confirm_token_hash,
	confirm_expires_at = EXCLUDED.confirm_expires_at,
	unsub_token = EXCLUDED.unsub_token,
	unsubscribed_at = NULL,
	updated_at = EXCLUDED.updated_at
RETURNING id, email, status, unsub_token, confirm_expires_at`, email, confirmHash, expiresAt, unsubToken, now).
			Scan(&sub.ID, &sub.Email, &sub.Status, &sub.UnsubToken, &sub.ConfirmExpires)
	})
	return sub, alreadyActive, err
}

// Confirm activates a pending subscription matching confirmHash while unexpired.
func (r NewsletterRepo) Confirm(ctx context.Context, confirmHash string, now time.Time) (newsletter.Subscriber, bool, error) {
	var sub newsletter.Subscriber
	err := r.DB.QueryRow(ctx, `
UPDATE newsletter_subscribers
SET status = 'active',
	confirm_token_hash = NULL,
	confirm_expires_at = NULL,
	confirmed_at = COALESCE(confirmed_at, $2),
	updated_at = $2
WHERE confirm_token_hash = $1
  AND status = 'pending'
  AND (confirm_expires_at IS NULL OR confirm_expires_at > $2)
RETURNING id, email, status, unsub_token, confirmed_at`, confirmHash, now).
		Scan(&sub.ID, &sub.Email, &sub.Status, &sub.UnsubToken, &sub.ConfirmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sub, false, nil
	}
	return sub, err == nil, err
}

// Unsubscribe marks a subscriber unsubscribed by permanent token.
func (r NewsletterRepo) Unsubscribe(ctx context.Context, unsubToken string, now time.Time) (bool, error) {
	tag, err := r.DB.Exec(ctx, `
UPDATE newsletter_subscribers
SET status = 'unsubscribed',
	confirm_token_hash = NULL,
	unsubscribed_at = $2,
	updated_at = $2
WHERE unsub_token = $1`, unsubToken, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ActiveSubscribers lists all confirmed subscribers ordered by id.
func (r NewsletterRepo) ActiveSubscribers(ctx context.Context) ([]newsletter.Subscriber, error) {
	rows, err := r.DB.Query(ctx, `
SELECT id, email, status, unsub_token, confirmed_at
FROM newsletter_subscribers
WHERE status = 'active'
ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (newsletter.Subscriber, error) {
		var s newsletter.Subscriber
		err := row.Scan(&s.ID, &s.Email, &s.Status, &s.UnsubToken, &s.ConfirmedAt)
		return s, err
	})
}

// TopCandidates returns top scored items since `since`, keeping AI score primary with bounded vote boost.
func (r NewsletterRepo) TopCandidates(ctx context.Context, since time.Time, threshold float64, limit int) ([]domain.FeedItem, error) {
	rows, err := r.DB.Query(ctx, `
SELECT `+feedCols+`
FROM feed_view
WHERE COALESCE(published_at, projected_at) >= $1
  AND score >= $2
ORDER BY score + GREATEST(-0.5, LEAST(0.5, (likes_count - dislikes_count)::double precision * 0.05)) DESC,
         score DESC, COALESCE(published_at, projected_at) DESC, item_id DESC
LIMIT $3`, since, threshold, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanFeed)
}

// HasDelivery reports whether an edition has already been sent for editionDate.
func (r NewsletterRepo) HasDelivery(ctx context.Context, editionDate string) (bool, error) {
	var exists bool
	err := r.DB.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1 FROM newsletter_deliveries
	WHERE edition_date = $1::date AND status IN ('ok', 'partial')
)`, editionDate).Scan(&exists)
	return exists, err
}

// RecordDelivery persists or updates the edition dispatch result.
func (r NewsletterRepo) RecordDelivery(ctx context.Context, d newsletter.Delivery) error {
	ids := d.ItemIDs
	if ids == nil {
		ids = []int64{}
	}
	_, err := r.DB.Exec(ctx, `
INSERT INTO newsletter_deliveries (edition_date, item_ids, sent_count, failed_count, status)
VALUES ($1::date, $2, $3, $4, $5)
ON CONFLICT (edition_date) DO UPDATE SET
	item_ids = EXCLUDED.item_ids,
	sent_count = EXCLUDED.sent_count,
	failed_count = EXCLUDED.failed_count,
	status = EXCLUDED.status,
	created_at = now()`, d.EditionDate, ids, d.SentCount, d.FailedCount, d.Status)
	return err
}
