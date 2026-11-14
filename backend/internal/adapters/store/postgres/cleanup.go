package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/retention"
)

// CleanupRepo executes the retention policy against Postgres.
type CleanupRepo struct{ DB *pgxpool.Pool }

const cleanupItemPredicate = `COALESCE(published_at, fetched_at) < $1`

func cleanupCounts(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, p retention.Params) (retention.Counts, error) {
	counts := retention.Counts{}
	queries := []struct {
		name  string
		query string
		args  []any
	}{
		{"items_total", `SELECT count(*) FROM items`, nil},
		{"items", `SELECT count(*) FROM items WHERE ` + cleanupItemPredicate, []any{p.Cutoff}},
		{"feed_view_total", `SELECT count(*) FROM feed_view`, nil},
		{"feed_view", `SELECT count(*) FROM feed_view v JOIN items i ON i.id = v.item_id WHERE COALESCE(i.published_at, i.fetched_at) < $1`, []any{p.Cutoff}},
		{"clusters", `SELECT count(*) FROM clusters c WHERE NOT EXISTS (
  SELECT 1 FROM items i
  WHERE i.cluster_id = c.id AND COALESCE(i.published_at, i.fetched_at) >= $1
)`, []any{p.Cutoff}},
		{"digests", `SELECT count(*) FROM digests WHERE digest_date < $1::date`, []any{p.CutoffDate}},
		{"fetch_runs", `SELECT count(*) FROM fetch_runs WHERE started_at < $1`, []any{p.Cutoff}},
		{"alert_events", `SELECT count(*) FROM alert_events WHERE at < $1`, []any{p.Cutoff}},
		{"ai_cache", `SELECT count(*) FROM ai_cache WHERE created_at < $1`, []any{p.Cutoff}},
		{"llm_usage", `SELECT count(*) FROM llm_usage WHERE day < $1::date`, []any{p.CutoffDate}},
	}
	for _, item := range queries {
		var n int64
		if err := q.QueryRow(ctx, item.query, item.args...).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", item.name, err)
		}
		counts[item.name] = n
	}
	return counts, nil
}

// Cleanup returns the would-be delete counts without mutation when DryRun is
// true. Real cleanup locks all affected tables, materializes candidate IDs,
// deletes dependent projections first, and commits the whole operation.
func (r CleanupRepo) Cleanup(ctx context.Context, p retention.Params) (retention.Counts, error) {
	if p.DryRun {
		return cleanupCounts(ctx, r.DB, p)
	}

	tx, err := r.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Keep a stable lock order and briefly exclude concurrent writes while the
	// candidate set and all dependent deletes are committed.
	if _, err := tx.Exec(ctx, `LOCK TABLE clusters, items, feed_view, digests, fetch_runs, alert_events, ai_cache, llm_usage IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, fmt.Errorf("lock cleanup tables: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE horizon_cleanup_items ON COMMIT DROP AS
SELECT id FROM items WHERE `+cleanupItemPredicate, p.Cutoff); err != nil {
		return nil, fmt.Errorf("materialize cleanup items: %w", err)
	}

	counts, err := cleanupCounts(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	deletes := []struct {
		name  string
		query string
	}{
		{"feed_view", `DELETE FROM feed_view v USING horizon_cleanup_items c WHERE v.item_id = c.id`},
		{"digests", `DELETE FROM digests WHERE digest_date < $1::date`},
		{"fetch_runs", `DELETE FROM fetch_runs WHERE started_at < $1`},
		{"alert_events", `DELETE FROM alert_events WHERE at < $1`},
		{"ai_cache", `DELETE FROM ai_cache WHERE created_at < $1`},
		{"llm_usage", `DELETE FROM llm_usage WHERE day < $1::date`},
		{"items", `DELETE FROM items i USING horizon_cleanup_items c WHERE i.id = c.id`},
		{"clusters", `DELETE FROM clusters c WHERE NOT EXISTS (SELECT 1 FROM items i WHERE i.cluster_id = c.id)`},
	}
	for _, item := range deletes {
		var tag pgconn.CommandTag
		if item.name == "feed_view" || item.name == "items" || item.name == "clusters" {
			tag, err = tx.Exec(ctx, item.query)
		} else {
			arg := p.Cutoff
			if item.name == "digests" || item.name == "llm_usage" {
				arg = p.CutoffDate
			}
			tag, err = tx.Exec(ctx, item.query, arg)
		}
		if err != nil {
			return nil, fmt.Errorf("delete %s: %w", item.name, err)
		}
		counts[item.name] = int64(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit cleanup: %w", err)
	}
	return counts, nil
}
