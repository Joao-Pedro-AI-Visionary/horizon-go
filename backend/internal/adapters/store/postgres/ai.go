package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/domain"
)

// dedupLock serializes cluster writes across processes.
const dedupLock = 7_202_602

// VectorLiteral renders v in pgvector text form: [1,2,3].
func VectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// ParseVector reads the pgvector text form.
func ParseVector(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimSuffix(s, "]"), "[")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("parse vector: %w", err)
		}
		out[i] = float32(f)
	}
	return out, nil
}

// AICache implements ports.AICache on the ai_cache table.
type AICache struct{ DB *pgxpool.Pool }

// Get returns the cached value of kind/key.
func (c AICache) Get(ctx context.Context, kind, key string) ([]byte, bool, error) {
	var v []byte
	err := c.DB.QueryRow(ctx, `SELECT value FROM ai_cache WHERE kind = $1 AND key = $2`, kind, key).Scan(&v)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// Put stores (or replaces) a value.
func (c AICache) Put(ctx context.Context, kind, key, model string, value []byte) error {
	_, err := c.DB.Exec(ctx, `
INSERT INTO ai_cache (kind, key, model, value) VALUES ($1, $2, $3, $4)
ON CONFLICT (kind, key) DO UPDATE SET value = EXCLUDED.value, model = EXCLUDED.model, created_at = now()`,
		kind, key, model, value)
	return err
}

// ScoreRow is what the scorer needs from an item.
type ScoreRow struct {
	ID         int64
	Title      string
	URL        string
	Content    string
	Author     string
	SourceKind string
	Scored     bool
}

// AIRepo holds the queries of the scoring and dedup stages.
type AIRepo struct{ DB *pgxpool.Pool }

// IDByURLHash resolves the item of a raw message.
func (r AIRepo) IDByURLHash(ctx context.Context, hash string) (int64, error) {
	var id int64
	err := r.DB.QueryRow(ctx, `SELECT id FROM items WHERE url_hash = $1`, hash).Scan(&id)
	return id, mapErr(err)
}

// PendingScore lists items without a score that still have attempts left.
func (r AIRepo) PendingScore(ctx context.Context, maxAttempts, limit int) ([]int64, error) {
	return r.ids(ctx, `SELECT id FROM items WHERE status = 'raw' AND score IS NULL AND score_attempts < $1 ORDER BY id LIMIT $2`, maxAttempts, limit)
}

// PendingDedup lists scored items not yet clustered.
func (r AIRepo) PendingDedup(ctx context.Context, limit int) ([]int64, error) {
	return r.ids(ctx, `SELECT id FROM items WHERE status = 'scored' AND embedding IS NOT NULL ORDER BY COALESCE(published_at, fetched_at), id LIMIT $1`, limit)
}

func (r AIRepo) ids(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := r.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// LoadScoreRow reads the analyzable fields of one item.
func (r AIRepo) LoadScoreRow(ctx context.Context, id int64) (ScoreRow, error) {
	var s ScoreRow
	err := r.DB.QueryRow(ctx, `
SELECT i.id, i.title, i.url, i.content, i.author, s.kind, i.score IS NOT NULL
FROM items i JOIN sources s ON s.id = i.source_id WHERE i.id = $1`, id).
		Scan(&s.ID, &s.Title, &s.URL, &s.Content, &s.Author, &s.SourceKind, &s.Scored)
	return s, mapErr(err)
}

// SaveScore writes the analysis and embedding once; it returns false when the
// item was already scored (another worker or a redelivery won).
func (r AIRepo) SaveScore(ctx context.Context, id int64, a domain.Analysis, emb []float32) (bool, error) {
	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}
	tag, err := r.DB.Exec(ctx, `
UPDATE items SET score = $2, score_reason = $3, summary = $4, tags = $5, embedding = $6::vector,
       status = 'scored', scored_at = now(), last_error = '', updated_at = now()
WHERE id = $1 AND score IS NULL`,
		id, a.Score, a.Reason, a.Summary, tags, VectorLiteral(emb))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RecordScoreFailure counts a failed attempt; at maxAttempts the item is
// marked 'failed' and leaves the queue.
func (r AIRepo) RecordScoreFailure(ctx context.Context, id int64, cause error, maxAttempts int) error {
	msg := cause.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_, err := r.DB.Exec(ctx, `
UPDATE items SET score_attempts = score_attempts + 1, last_error = $2,
       status = CASE WHEN score_attempts + 1 >= $3 THEN 'failed' ELSE status END, updated_at = now()
WHERE id = $1 AND score IS NULL`, id, msg, maxAttempts)
	return err
}

// DedupRow is an item as seen by the dedup stage.
type DedupRow struct {
	ID          int64
	ClusterID   int64
	Status      string
	Title       string
	Summary     string
	Tags        []string
	Score       float64
	PublishedAt time.Time // published_at, or fetched_at when unknown
	Embedding   []float32
	Similarity  float64 // only set by Neighbors
}

// Member converts the row for the clustering logic.
func (d DedupRow) Member() domain.Member {
	return domain.Member{ItemID: d.ID, ClusterID: d.ClusterID, Score: d.Score, PublishedAt: d.PublishedAt}
}

// LoadDedupRow reads one item with its embedding.
func (r AIRepo) LoadDedupRow(ctx context.Context, id int64) (DedupRow, error) {
	var d DedupRow
	var emb *string
	var cluster *int64
	var score *float64
	err := r.DB.QueryRow(ctx, `
SELECT id, cluster_id, status, title, COALESCE(summary, ''), tags, score, COALESCE(published_at, fetched_at), embedding::text
FROM items WHERE id = $1`, id).
		Scan(&d.ID, &cluster, &d.Status, &d.Title, &d.Summary, &d.Tags, &score, &d.PublishedAt, &emb)
	if err != nil {
		return d, mapErr(err)
	}
	if cluster != nil {
		d.ClusterID = *cluster
	}
	if score != nil {
		d.Score = *score
	}
	if emb != nil {
		if d.Embedding, err = ParseVector(*emb); err != nil {
			return d, err
		}
	}
	return d, nil
}

// Neighbors returns up to k already-clustered items ordered by cosine
// similarity to emb (HNSW index, vector_cosine_ops) whose event time is within
// window of at. Iterative scan keeps the filter from starving the index scan.
func (r AIRepo) Neighbors(ctx context.Context, id int64, emb []float32, at time.Time, window time.Duration, k int) ([]DedupRow, error) {
	tx, err := r.DB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
SELECT id, COALESCE(cluster_id, 0), status, title, COALESCE(summary, ''), tags, COALESCE(score, 0),
       COALESCE(published_at, fetched_at), 1 - (embedding <=> $1::vector) AS sim
FROM items
WHERE id <> $2 AND embedding IS NOT NULL AND status IN ('deduped', 'duplicate')
  AND COALESCE(published_at, fetched_at) BETWEEN $3::timestamptz - make_interval(secs => $4) AND $3::timestamptz + make_interval(secs => $4)
ORDER BY embedding <=> $1::vector
LIMIT $5`, VectorLiteral(emb), id, at, window.Seconds(), k)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DedupRow, error) {
		var d DedupRow
		err := row.Scan(&d.ID, &d.ClusterID, &d.Status, &d.Title, &d.Summary, &d.Tags, &d.Score, &d.PublishedAt, &d.Similarity)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// ClusterResult is the outcome of ApplyCluster.
type ClusterResult struct {
	ClusterID int64
	PrimaryID int64
	Size      int
	Merged    []int64 // clusters absorbed into ClusterID
	Skipped   bool    // item was already clustered
}

// ApplyCluster puts item id into a cluster together with the matched items
// (none -> a new cluster), keeps the highest score as primary, refreshes the
// centroid and rewrites the feed_view projection of the cluster. Everything
// runs in one transaction under an advisory lock.
func (r AIRepo) ApplyCluster(ctx context.Context, id int64, matched []int64) (ClusterResult, error) {
	var res ClusterResult
	err := pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, dedupLock); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM items WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
			return mapErr(err)
		}
		if status != "scored" {
			res.Skipped = true
			return nil
		}

		// Current clusters of the matched items (read under the lock).
		var ms []domain.Member
		if len(matched) > 0 {
			rows, err := tx.Query(ctx, `SELECT id, COALESCE(cluster_id, 0) FROM items WHERE id = ANY($1) AND cluster_id IS NOT NULL`, matched)
			if err != nil {
				return err
			}
			ms, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Member, error) {
				var m domain.Member
				return m, row.Scan(&m.ItemID, &m.ClusterID)
			})
			if err != nil {
				return err
			}
		}
		plan := domain.PlanMerge(ms)
		target := plan.Target
		if target == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO clusters (title) SELECT title FROM items WHERE id = $1 RETURNING id`, id).Scan(&target); err != nil {
				return err
			}
		}
		touched := append([]int64{target}, plan.Absorb...)
		if _, err := tx.Exec(ctx, `DELETE FROM feed_view WHERE cluster_id = ANY($1) OR item_id = $2`, touched, id); err != nil {
			return err
		}
		if len(plan.Absorb) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE items SET cluster_id = $1, updated_at = now() WHERE cluster_id = ANY($2)`, target, plan.Absorb); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM clusters WHERE id = ANY($1)`, plan.Absorb); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE items SET cluster_id = $1, deduped_at = now(), updated_at = now() WHERE id = $2`, target, id); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `SELECT id, cluster_id, COALESCE(score, 0), COALESCE(published_at, fetched_at) FROM items WHERE cluster_id = $1`, target)
		if err != nil {
			return err
		}
		members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Member, error) {
			var m domain.Member
			return m, row.Scan(&m.ItemID, &m.ClusterID, &m.Score, &m.PublishedAt)
		})
		if err != nil {
			return err
		}
		primary := domain.PickPrimary(members)
		if _, err := tx.Exec(ctx, `
UPDATE items SET status = CASE WHEN id = $2 THEN 'deduped' ELSE 'duplicate' END, updated_at = now()
WHERE cluster_id = $1`, target, primary.ItemID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
UPDATE clusters c SET representative_item_id = $2,
       title = (SELECT title FROM items WHERE id = $2),
       item_count = $3,
       centroid = (SELECT avg(embedding) FROM items WHERE cluster_id = $1),
       updated_at = now()
WHERE c.id = $1`, target, primary.ItemID, len(members)); err != nil {
			return err
		}
		// CQRS read model: one row per cluster, the primary item.
		if _, err := tx.Exec(ctx, `
INSERT INTO feed_view (item_id, source_id, source_kind, source_name, title, url, summary, score_reason, score, tags, published_at, cluster_id, duplicate_count, projected_at)
SELECT i.id, s.id, s.kind, s.name, i.title, i.url, COALESCE(i.summary, ''), COALESCE(i.score_reason, ''), i.score, i.tags, i.published_at, c.id, c.item_count - 1, now()
FROM clusters c JOIN items i ON i.id = c.representative_item_id JOIN sources s ON s.id = i.source_id
WHERE c.id = $1`, target); err != nil {
			return err
		}
		res = ClusterResult{ClusterID: target, PrimaryID: primary.ItemID, Size: len(members), Merged: plan.Absorb}
		return nil
	})
	return res, err
}
