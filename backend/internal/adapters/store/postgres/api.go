package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/domain"
)

// ReadRepo holds the queries of the REST API and the digest generator.
type ReadRepo struct{ DB *pgxpool.Pool }

// Ping checks the connection.
func (r ReadRepo) Ping(ctx context.Context) error { return r.DB.Ping(ctx) }

const feedCols = `item_id, source_id, source_kind, source_name, title, url, summary, score_reason, score, tags, published_at, cluster_id, duplicate_count, projected_at`

func scanFeed(row pgx.CollectableRow) (domain.FeedItem, error) {
	var f domain.FeedItem
	err := row.Scan(&f.ItemID, &f.SourceID, &f.SourceKind, &f.SourceName, &f.Title, &f.URL, &f.Summary, &f.ScoreReason,
		&f.Score, &f.Tags, &f.PublishedAt, &f.ClusterID, &f.DuplicateCount, &f.ProjectedAt)
	if f.Tags == nil {
		f.Tags = []string{}
	}
	return f, err
}

// Feed pages through the feed_view projection (the only table it reads).
func (r ReadRepo) Feed(ctx context.Context, q domain.FeedQuery) (domain.FeedPage, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if q.SourceKind != "" {
		add("source_kind = $%d", q.SourceKind)
	}
	if q.SourceID > 0 {
		add("source_id = $%d", q.SourceID)
	}
	if q.Tag != "" {
		add("$%d = ANY(tags)", strings.ToLower(q.Tag))
	}
	if q.MinScore != nil {
		add("score >= $%d", *q.MinScore)
	}
	if q.Q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Q) + "%"
		add("(title ILIKE $%[1]d OR summary ILIKE $%[1]d)", like)
	}
	if !q.From.IsZero() {
		add("COALESCE(published_at, projected_at) >= $%d", q.From)
	}
	if !q.To.IsZero() {
		add("COALESCE(published_at, projected_at) < $%d", q.To)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	order := "score DESC, COALESCE(published_at, projected_at) DESC, item_id DESC"
	if q.Sort == domain.SortRecent {
		order = "COALESCE(published_at, projected_at) DESC, score DESC, item_id DESC"
	}
	page := domain.FeedPage{Page: q.Page, PageSize: q.PageSize, Items: []domain.FeedItem{}}
	if err := r.DB.QueryRow(ctx, `SELECT count(*) FROM feed_view`+cond, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := r.DB.Query(ctx, fmt.Sprintf(`SELECT %s FROM feed_view%s ORDER BY %s LIMIT $%d OFFSET $%d`,
		feedCols, cond, order, len(args)-1, len(args)), args...)
	if err != nil {
		return page, err
	}
	items, err := pgx.CollectRows(rows, scanFeed)
	if err != nil {
		return page, err
	}
	if items != nil {
		page.Items = items
	}
	return page, nil
}

// DigestCandidates returns the feed rows whose event time is in [from, to).
func (r ReadRepo) DigestCandidates(ctx context.Context, from, to time.Time) ([]domain.FeedItem, error) {
	rows, err := r.DB.Query(ctx, `SELECT `+feedCols+` FROM feed_view
WHERE COALESCE(published_at, projected_at) >= $1 AND COALESCE(published_at, projected_at) < $2
ORDER BY score DESC, item_id`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanFeed)
}

// Item returns one item with its cluster and the other cluster members.
func (r ReadRepo) Item(ctx context.Context, id int64) (domain.ItemDetail, error) {
	var d domain.ItemDetail
	var clusterID, repID *int64
	var clusterTitle *string
	var clusterCount *int
	err := r.DB.QueryRow(ctx, `
SELECT i.id, i.source_id, s.kind, s.name, i.title, i.url, i.author, i.content, i.status, i.score,
       COALESCE(i.score_reason, ''), COALESCE(i.summary, ''), i.tags, i.published_at, i.fetched_at, i.scored_at, i.last_error,
       i.cluster_id, c.title, c.item_count, c.representative_item_id
FROM items i JOIN sources s ON s.id = i.source_id LEFT JOIN clusters c ON c.id = i.cluster_id
WHERE i.id = $1`, id).Scan(&d.ID, &d.SourceID, &d.SourceKind, &d.SourceName, &d.Title, &d.URL, &d.Author, &d.Content,
		&d.Status, &d.Score, &d.ScoreReason, &d.Summary, &d.Tags, &d.PublishedAt, &d.FetchedAt, &d.ScoredAt, &d.LastError,
		&clusterID, &clusterTitle, &clusterCount, &repID)
	if err != nil {
		return d, mapErr(err)
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	d.Duplicates = []domain.RelatedItem{}
	if clusterID == nil {
		return d, nil
	}
	d.Cluster = &domain.ClusterInfo{ID: *clusterID, Title: *clusterTitle, ItemCount: *clusterCount, RepresentativeItemID: repID}
	d.IsPrimary = repID != nil && *repID == d.ID
	rows, err := r.DB.Query(ctx, `
SELECT o.id, o.title, o.url, s.kind, s.name, o.score, o.status, o.published_at,
       CASE WHEN o.embedding IS NULL OR i.embedding IS NULL THEN NULL
            ELSE (1 - (o.embedding <=> i.embedding))::float8 END
FROM items i
JOIN items o ON o.cluster_id = i.cluster_id AND o.id <> i.id
JOIN sources s ON s.id = o.source_id
WHERE i.id = $1
ORDER BY o.score DESC NULLS LAST, o.id`, id)
	if err != nil {
		return d, err
	}
	dups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.RelatedItem, error) {
		var x domain.RelatedItem
		var score *float32
		err := row.Scan(&x.ID, &x.Title, &x.URL, &x.SourceKind, &x.SourceName, &score, &x.Status, &x.PublishedAt, &x.Similarity)
		if score != nil {
			v := float64(*score)
			x.Score = &v
		}
		return x, err
	})
	if err != nil {
		return d, err
	}
	if dups != nil {
		d.Duplicates = dups
	}
	return d, nil
}

const settingsCols = `score_threshold, digest_max_items, digest_per_source, digest_cron, timezone, updated_at`

func scanSettings(row pgx.Row) (domain.Settings, error) {
	var s domain.Settings
	err := row.Scan(&s.ScoreThreshold, &s.DigestMaxItems, &s.DigestPerSource, &s.DigestCron, &s.Timezone, &s.UpdatedAt)
	return s, mapErr(err)
}

// GetSettings reads the settings row.
func (r ReadRepo) GetSettings(ctx context.Context) (domain.Settings, error) {
	return scanSettings(r.DB.QueryRow(ctx, `SELECT `+settingsCols+` FROM settings WHERE id = 1`))
}

// PutSettings replaces the settings row.
func (r ReadRepo) PutSettings(ctx context.Context, s domain.Settings) (domain.Settings, error) {
	return scanSettings(r.DB.QueryRow(ctx, `
INSERT INTO settings (id, score_threshold, digest_max_items, digest_per_source, digest_cron, timezone, updated_at)
VALUES (1, $1, $2, $3, $4, $5, now())
ON CONFLICT (id) DO UPDATE SET score_threshold = EXCLUDED.score_threshold, digest_max_items = EXCLUDED.digest_max_items,
    digest_per_source = EXCLUDED.digest_per_source, digest_cron = EXCLUDED.digest_cron, timezone = EXCLUDED.timezone, updated_at = now()
RETURNING `+settingsCols, s.ScoreThreshold, s.DigestMaxItems, s.DigestPerSource, s.DigestCron, s.Timezone))
}

const digestCols = `id, to_char(digest_date, 'YYYY-MM-DD'), language, title, item_ids, considered, window_start, window_end, created_at, updated_at`

func scanDigest(row pgx.Row, full bool) (domain.Digest, error) {
	var d domain.Digest
	var ws, we *time.Time
	dest := []any{&d.ID, &d.Date, &d.Language, &d.Title, &d.ItemIDs, &d.Considered, &ws, &we, &d.CreatedAt, &d.UpdatedAt}
	if full {
		dest = append(dest, &d.Markdown, &d.HTML)
	}
	if err := row.Scan(dest...); err != nil {
		return d, mapErr(err)
	}
	if d.ItemIDs == nil {
		d.ItemIDs = []int64{}
	}
	d.ItemCount = len(d.ItemIDs)
	if ws != nil {
		d.WindowStart = *ws
	}
	if we != nil {
		d.WindowEnd = *we
	}
	return d, nil
}

// SaveDigest upserts a digest by (date, language).
func (r ReadRepo) SaveDigest(ctx context.Context, d domain.Digest) (domain.Digest, error) {
	ids := d.ItemIDs
	if ids == nil {
		ids = []int64{}
	}
	return scanDigest(r.DB.QueryRow(ctx, `
INSERT INTO digests (digest_date, language, title, markdown, html, item_ids, considered, window_start, window_end)
VALUES ($1::date, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (digest_date, language) DO UPDATE SET title = EXCLUDED.title, markdown = EXCLUDED.markdown, html = EXCLUDED.html,
    item_ids = EXCLUDED.item_ids, considered = EXCLUDED.considered, window_start = EXCLUDED.window_start,
    window_end = EXCLUDED.window_end, updated_at = now()
RETURNING `+digestCols+`, markdown, html`,
		d.Date, d.Language, d.Title, d.Markdown, d.HTML, ids, d.Considered, d.WindowStart, d.WindowEnd), true)
}

// ListDigests returns the digests, newest date first, without their bodies.
func (r ReadRepo) ListDigests(ctx context.Context, limit int) ([]domain.Digest, error) {
	rows, err := r.DB.Query(ctx, `SELECT `+digestCols+` FROM digests ORDER BY digest_date DESC, language LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Digest{}
	for rows.Next() {
		d, err := scanDigest(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDigest returns one digest with its Markdown and HTML.
func (r ReadRepo) GetDigest(ctx context.Context, date, language string) (domain.Digest, error) {
	return scanDigest(r.DB.QueryRow(ctx, `SELECT `+digestCols+`, markdown, html FROM digests WHERE digest_date = $1::date AND language = $2`,
		date, language), true)
}

// SourceStatus is a source with its item count and last fetch run.
type SourceStatus struct {
	domain.SourceConfig
	Items     int
	LastFetch *domain.FetchRun
}

// ListSourceStatus returns every source with its item count and last fetch.
func (r ReadRepo) ListSourceStatus(ctx context.Context) ([]SourceStatus, error) {
	rows, err := r.DB.Query(ctx, `
SELECT s.id, s.kind, s.name, s.config, s.enabled, s.created_at, s.updated_at,
       (SELECT count(*) FROM items i WHERE i.source_id = s.id),
       f.id, f.run_id, f.status, f.item_count, f.error, f.started_at, f.finished_at
FROM sources s
LEFT JOIN LATERAL (SELECT * FROM fetch_runs WHERE source_id = s.id ORDER BY started_at DESC, id DESC LIMIT 1) f ON true
ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceStatus{}
	for rows.Next() {
		var s SourceStatus
		var cfg []byte
		var fid *int64
		var runID, status, ferr *string
		var count *int
		var started, finished *time.Time
		if err := rows.Scan(&s.ID, &s.Kind, &s.Name, &cfg, &s.Enabled, &s.CreatedAt, &s.UpdatedAt, &s.Items,
			&fid, &runID, &status, &count, &ferr, &started, &finished); err != nil {
			return nil, err
		}
		s.Config = cfg
		if fid != nil {
			s.LastFetch = &domain.FetchRun{ID: *fid, RunID: *runID, SourceID: s.ID, SourceName: s.Name, Status: *status,
				ItemCount: *count, Error: *ferr, StartedAt: *started, FinishedAt: *finished}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UsageRepo records model token usage.
type UsageRepo struct{ DB *pgxpool.Pool }

// Record adds one call to today's row (day in the settings timezone).
func (r UsageRepo) Record(ctx context.Context, u domain.TokenUsage) error {
	_, err := r.DB.Exec(ctx, `
INSERT INTO llm_usage (day, kind, model, calls, prompt_tokens, completion_tokens)
VALUES ((now() AT TIME ZONE COALESCE((SELECT timezone FROM settings WHERE id = 1), 'UTC'))::date, $1, $2, 1, $3, $4)
ON CONFLICT (day, kind, model) DO UPDATE SET calls = llm_usage.calls + 1,
    prompt_tokens = llm_usage.prompt_tokens + EXCLUDED.prompt_tokens,
    completion_tokens = llm_usage.completion_tokens + EXCLUDED.completion_tokens`,
		u.Kind, u.Model, u.PromptTokens, u.CompletionTokens)
	return err
}

// Metrics aggregates the last days calendar days (settings timezone).
func (r ReadRepo) Metrics(ctx context.Context, days int) (domain.Metrics, error) {
	st, err := r.GetSettings(ctx)
	if err != nil {
		return domain.Metrics{}, err
	}
	loc, err := time.LoadLocation(st.Timezone)
	if err != nil {
		return domain.Metrics{}, err
	}
	now := time.Now().In(loc)
	y, mo, d := now.Date()
	since := time.Date(y, mo, d-(days-1), 0, 0, 0, 0, loc)
	m := domain.Metrics{Days: days, Timezone: st.Timezone, Status: map[string]int{}, GeneratedAt: now}

	rows, err := r.DB.Query(ctx, `
WITH d AS (SELECT generate_series($1::date, $2::date, interval '1 day')::date AS day),
f AS (SELECT (created_at AT TIME ZONE $3)::date AS day, count(*) AS n FROM items WHERE created_at >= $4 GROUP BY 1),
s AS (SELECT (scored_at AT TIME ZONE $3)::date AS day, count(*) AS n FROM items WHERE scored_at >= $4 GROUP BY 1)
SELECT to_char(d.day, 'YYYY-MM-DD'), COALESCE(f.n, 0)::int, COALESCE(s.n, 0)::int
FROM d LEFT JOIN f USING (day) LEFT JOIN s USING (day) ORDER BY d.day`,
		since.Format(time.DateOnly), now.Format(time.DateOnly), st.Timezone, since)
	if err != nil {
		return m, err
	}
	if m.ItemsPerDay, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.DayCount, error) {
		var x domain.DayCount
		return x, row.Scan(&x.Day, &x.Fetched, &x.Scored)
	}); err != nil {
		return m, err
	}

	rows, err = r.DB.Query(ctx, `
SELECT s.id, s.name, s.kind, s.enabled,
       (SELECT count(*) FROM items i WHERE i.source_id = s.id AND i.created_at >= $1)::int,
       (SELECT count(*) FROM feed_view v WHERE v.source_id = s.id AND COALESCE(v.published_at, v.projected_at) >= $1)::int,
       (SELECT round(avg(i.score)::numeric, 2)::float8 FROM items i WHERE i.source_id = s.id AND i.created_at >= $1),
       (SELECT count(*) FROM fetch_runs r WHERE r.source_id = s.id AND r.started_at >= $1 AND r.status = 'ok')::int,
       (SELECT count(*) FROM fetch_runs r WHERE r.source_id = s.id AND r.started_at >= $1 AND r.status = 'error')::int,
       l.started_at, COALESCE(l.status, ''), COALESCE(l.error, '')
FROM sources s
LEFT JOIN LATERAL (SELECT started_at, status, error FROM fetch_runs WHERE source_id = s.id ORDER BY started_at DESC, id DESC LIMIT 1) l ON true
ORDER BY s.id`, since)
	if err != nil {
		return m, err
	}
	if m.BySource, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SourceMetrics, error) {
		var x domain.SourceMetrics
		return x, row.Scan(&x.SourceID, &x.Name, &x.Kind, &x.Enabled, &x.Items, &x.FeedItems, &x.AvgScore,
			&x.FetchOK, &x.FetchErrors, &x.LastFetchAt, &x.LastStatus, &x.LastError)
	}); err != nil {
		return m, err
	}

	rows, err = r.DB.Query(ctx, `SELECT status, count(*)::int FROM items WHERE created_at >= $1 GROUP BY status`, since)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return m, err
		}
		m.Status[k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return m, err
	}

	if err := r.DB.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE i.cluster_id IS NOT NULL)::int,
       count(*) FILTER (WHERE i.cluster_id IS NOT NULL AND i.id <> c.representative_item_id)::int,
       count(DISTINCT i.cluster_id)::int,
       count(DISTINCT i.cluster_id) FILTER (WHERE c.item_count > 1)::int,
       count(*) FILTER (WHERE i.status = 'failed')::int,
       count(*) FILTER (WHERE i.status = 'raw' AND i.last_error <> '')::int
FROM items i LEFT JOIN clusters c ON c.id = i.cluster_id
WHERE i.created_at >= $1`, since).Scan(&m.Dedup.Clustered, &m.Dedup.Duplicates, &m.Dedup.Clusters,
		&m.Dedup.MultiItemClusters, &m.Failures.ScoreFailed, &m.Failures.ScoreRetries); err != nil {
		return m, err
	}
	if m.Dedup.Clustered > 0 {
		m.Dedup.Rate = float64(m.Dedup.Duplicates) / float64(m.Dedup.Clustered)
	}

	if err := r.DB.QueryRow(ctx, `SELECT count(*)::int FROM fetch_runs WHERE started_at >= $1 AND status = 'error'`, since).
		Scan(&m.Failures.FetchErrors); err != nil {
		return m, err
	}
	rows, err = r.DB.Query(ctx, `
SELECT f.source_id, s.name, f.error, f.started_at FROM fetch_runs f JOIN sources s ON s.id = f.source_id
WHERE f.started_at >= $1 AND f.status = 'error' ORDER BY f.started_at DESC, f.id DESC LIMIT 20`, since)
	if err != nil {
		return m, err
	}
	if m.Failures.Recent, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.FetchFailure, error) {
		var x domain.FetchFailure
		return x, row.Scan(&x.SourceID, &x.SourceName, &x.Error, &x.At)
	}); err != nil {
		return m, err
	}
	if m.Failures.Recent == nil {
		m.Failures.Recent = []domain.FetchFailure{}
	}

	rows, err = r.DB.Query(ctx, `
SELECT to_char(day, 'YYYY-MM-DD'), kind, model, calls, prompt_tokens, completion_tokens
FROM llm_usage WHERE day >= $1::date ORDER BY day, kind, model`, since.Format(time.DateOnly))
	if err != nil {
		return m, err
	}
	if m.Tokens.PerDay, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.TokenDay, error) {
		var x domain.TokenDay
		return x, row.Scan(&x.Day, &x.Kind, &x.Model, &x.Calls, &x.PromptTokens, &x.CompletionTokens)
	}); err != nil {
		return m, err
	}
	if m.Tokens.PerDay == nil {
		m.Tokens.PerDay = []domain.TokenDay{}
	}
	for _, t := range m.Tokens.PerDay {
		m.Tokens.Calls += t.Calls
		m.Tokens.PromptTokens += t.PromptTokens
		m.Tokens.CompletionTokens += t.CompletionTokens
	}
	m.Tokens.TotalTokens = m.Tokens.PromptTokens + m.Tokens.CompletionTokens
	return m, nil
}
