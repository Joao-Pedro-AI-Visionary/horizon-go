package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/domain"
)

// VoteRepo persists anonymous per-device item votes and enforces short-lived
// salted-IP rate limits (never storing raw IPs).
type VoteRepo struct{ DB *pgxpool.Pool }

// CheckAndRecordRate prunes expired rate-limit records, checks if ipHash is
// below maxVotes within window at now, and records this attempt with ttl.
func (r VoteRepo) CheckAndRecordRate(ctx context.Context, ipHash string, window time.Duration, maxVotes int, ttl time.Duration, now time.Time) (time.Duration, bool, error) {
	if ipHash == "" || maxVotes <= 0 || window <= 0 {
		return 0, true, nil
	}
	now = now.UTC()
	var retryAfter time.Duration
	var allowed bool
	err := pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM vote_rate_limits WHERE expires_at <= $1`, now); err != nil {
			return err
		}
		since := now.Add(-window)
		var count int
		var oldest *time.Time
		if err := tx.QueryRow(ctx, `
SELECT count(*)::int, min(created_at)
FROM vote_rate_limits
WHERE ip_hash = $1 AND created_at > $2`, ipHash, since).Scan(&count, &oldest); err != nil {
			return err
		}
		if count >= maxVotes {
			allowed = false
			if oldest != nil {
				rem := window - now.Sub(*oldest)
				if rem < time.Second {
					rem = time.Second
				}
				retryAfter = rem
			} else {
				retryAfter = window
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO vote_rate_limits (ip_hash, created_at, expires_at)
VALUES ($1, $2, $3)`, ipHash, now, now.Add(ttl)); err != nil {
			return err
		}
		allowed = true
		return nil
	})
	return retryAfter, allowed, err
}

// Vote sets (1 or -1) or clears (0) the caller's vote on itemID and refreshes
// the cached feed_view counts inside one transaction. Dislikes remain hidden.
func (r VoteRepo) Vote(ctx context.Context, itemID int64, deviceHash string, vote int) (domain.VoteResult, error) {
	if vote != -1 && vote != 0 && vote != 1 {
		return domain.VoteResult{}, domain.Invalidf("vote must be -1, 0 or 1")
	}
	if deviceHash == "" {
		return domain.VoteResult{}, domain.Invalidf("device identifier required")
	}
	res := domain.VoteResult{ItemID: itemID, UserVote: vote}
	err := pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		var exists int64
		if err := tx.QueryRow(ctx, `SELECT id FROM items WHERE id = $1 FOR UPDATE`, itemID).Scan(&exists); err != nil {
			return mapErr(err)
		}
		if vote == 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM item_votes WHERE item_id = $1 AND device_hash = $2`, itemID, deviceHash); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `
INSERT INTO item_votes (item_id, device_hash, vote, created_at, updated_at)
VALUES ($1, $2, $3, now(), now())
ON CONFLICT (item_id, device_hash)
DO UPDATE SET vote = EXCLUDED.vote, updated_at = now()`, itemID, deviceHash, vote); err != nil {
				return err
			}
		}
		var likes, dislikes int
		if err := tx.QueryRow(ctx, `
SELECT
	COALESCE(count(*) FILTER (WHERE vote = 1), 0)::int,
	COALESCE(count(*) FILTER (WHERE vote = -1), 0)::int
FROM item_votes
WHERE item_id = $1`, itemID).Scan(&likes, &dislikes); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE feed_view SET likes_count = $2, dislikes_count = $3 WHERE item_id = $1`, itemID, likes, dislikes); err != nil {
			return err
		}
		res.Likes = likes
		return nil
	})
	return res, err
}

// UserVote looks up the caller's existing vote for one item (-1, 0, or 1).
func (r VoteRepo) UserVote(ctx context.Context, itemID int64, deviceHash string) (int, error) {
	if deviceHash == "" {
		return 0, nil
	}
	var vote int
	err := r.DB.QueryRow(ctx, `SELECT vote FROM item_votes WHERE item_id = $1 AND device_hash = $2`, itemID, deviceHash).Scan(&vote)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return vote, err
}
