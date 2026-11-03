// Package postgres implements the store ports on PostgreSQL (pgx).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/domain"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("postgres: not found")

// ErrConflict is returned on a unique violation (e.g. duplicate kind+name).
var ErrConflict = errors.New("postgres: conflict")

// Connect opens a pool and pings it.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
	}
	return err
}

// SourceRepo implements ports.SourceRepo.
type SourceRepo struct{ DB *pgxpool.Pool }

const sourceCols = `id, kind, name, config, enabled, created_at, updated_at`

func scanSource(row pgx.Row) (domain.SourceConfig, error) {
	var s domain.SourceConfig
	var cfg []byte
	err := row.Scan(&s.ID, &s.Kind, &s.Name, &cfg, &s.Enabled, &s.CreatedAt, &s.UpdatedAt)
	s.Config = json.RawMessage(cfg)
	return s, mapErr(err)
}

func configOrEmpty(c json.RawMessage) []byte {
	if len(c) == 0 {
		return []byte("{}")
	}
	return c
}

// Create inserts a source.
func (r SourceRepo) Create(ctx context.Context, s domain.SourceConfig) (domain.SourceConfig, error) {
	return scanSource(r.DB.QueryRow(ctx,
		`INSERT INTO sources (kind, name, config, enabled) VALUES ($1, $2, $3, $4) RETURNING `+sourceCols,
		s.Kind, s.Name, configOrEmpty(s.Config), s.Enabled))
}

// Ensure inserts the source if (kind, name) does not exist and returns the row.
func (r SourceRepo) Ensure(ctx context.Context, s domain.SourceConfig) (domain.SourceConfig, error) {
	if _, err := r.DB.Exec(ctx,
		`INSERT INTO sources (kind, name, config, enabled) VALUES ($1, $2, $3, $4) ON CONFLICT (kind, name) DO NOTHING`,
		s.Kind, s.Name, configOrEmpty(s.Config), s.Enabled); err != nil {
		return domain.SourceConfig{}, err
	}
	return scanSource(r.DB.QueryRow(ctx, `SELECT `+sourceCols+` FROM sources WHERE kind = $1 AND name = $2`, s.Kind, s.Name))
}

// Get returns one source by id.
func (r SourceRepo) Get(ctx context.Context, id int64) (domain.SourceConfig, error) {
	return scanSource(r.DB.QueryRow(ctx, `SELECT `+sourceCols+` FROM sources WHERE id = $1`, id))
}

// List returns sources ordered by id.
func (r SourceRepo) List(ctx context.Context, enabledOnly bool) ([]domain.SourceConfig, error) {
	rows, err := r.DB.Query(ctx, `SELECT `+sourceCols+` FROM sources WHERE ($1 = false OR enabled) ORDER BY id`, enabledOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SourceConfig
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Update replaces kind, name, config and enabled.
func (r SourceRepo) Update(ctx context.Context, s domain.SourceConfig) (domain.SourceConfig, error) {
	return scanSource(r.DB.QueryRow(ctx,
		`UPDATE sources SET kind = $2, name = $3, config = $4, enabled = $5, updated_at = now() WHERE id = $1 RETURNING `+sourceCols,
		s.ID, s.Kind, s.Name, configOrEmpty(s.Config), s.Enabled))
}

// Delete removes a source (items and fetch runs cascade).
func (r SourceRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.DB.Exec(ctx, `DELETE FROM sources WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ItemRepo implements ports.ItemRepo.
type ItemRepo struct{ DB *pgxpool.Pool }

// Upsert inserts the item or refreshes the existing row with the same
// url_hash. Scoring fields and status are never touched here; the richer
// content wins.
func (r ItemRepo) Upsert(ctx context.Context, it domain.Item) (int64, error) {
	if it.URLHash == "" {
		if err := it.Prepare(); err != nil {
			return 0, err
		}
	}
	var published *time.Time
	if !it.PublishedAt.IsZero() {
		published = &it.PublishedAt
	}
	fetched := it.FetchedAt
	if fetched.IsZero() {
		fetched = time.Now().UTC()
	}
	var id int64
	err := r.DB.QueryRow(ctx, `
INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, content, author, published_at, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (url_hash) DO UPDATE SET
    title        = EXCLUDED.title,
    content      = CASE WHEN length(EXCLUDED.content) > length(items.content) THEN EXCLUDED.content ELSE items.content END,
    author       = CASE WHEN items.author = '' THEN EXCLUDED.author ELSE items.author END,
    published_at = COALESCE(items.published_at, EXCLUDED.published_at),
    fetched_at   = EXCLUDED.fetched_at,
    updated_at   = now()
RETURNING id`,
		it.SourceID, it.ExternalID, it.URL, it.URLNormalized, it.URLHash, it.Title, it.Content, it.Author, published, fetched).Scan(&id)
	return id, mapErr(err)
}

// FetchRunRepo implements ports.FetchRunRepo.
type FetchRunRepo struct{ DB *pgxpool.Pool }

// Insert stores one per-source report.
func (r FetchRunRepo) Insert(ctx context.Context, f domain.FetchRun) (int64, error) {
	var id int64
	err := r.DB.QueryRow(ctx,
		`INSERT INTO fetch_runs (run_id, source_id, status, item_count, error, started_at, finished_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		f.RunID, f.SourceID, f.Status, f.ItemCount, f.Error, f.StartedAt, f.FinishedAt).Scan(&id)
	return id, mapErr(err)
}

// ListByRun returns the reports of one run, joined with the source name.
func (r FetchRunRepo) ListByRun(ctx context.Context, runID string) ([]domain.FetchRun, error) {
	rows, err := r.DB.Query(ctx, `
SELECT f.id, f.run_id, f.source_id, s.name, f.status, f.item_count, f.error, f.started_at, f.finished_at
FROM fetch_runs f JOIN sources s ON s.id = f.source_id
WHERE f.run_id = $1 ORDER BY f.source_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FetchRun
	for rows.Next() {
		var f domain.FetchRun
		if err := rows.Scan(&f.ID, &f.RunID, &f.SourceID, &f.SourceName, &f.Status, &f.ItemCount, &f.Error, &f.StartedAt, &f.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
