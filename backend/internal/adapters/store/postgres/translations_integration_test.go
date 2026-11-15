package postgres

// Integration test of the translation backfill queries against the real
// Postgres from CLAUDE.md, on a dedicated database. Migration 00015 (owned by
// the pipeline branch) adds the columns; until both branches are merged the
// test applies the same DDL with IF NOT EXISTS, a no-op once 00015 ran.
// When the infra is unreachable the test FAILS (no silent green).

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/migrations"
)

const testTranslationsDBName = "horizon_test_translations"

// translationColumnsDDL mirrors migrations/00015_item_translations.sql.
const translationColumnsDDL = `
ALTER TABLE items
    ADD COLUMN IF NOT EXISTS title_pt           TEXT,
    ADD COLUMN IF NOT EXISTS summary_pt         TEXT,
    ADD COLUMN IF NOT EXISTS pt_source          TEXT CHECK (pt_source IN ('llm', 'copy')),
    ADD COLUMN IF NOT EXISTS translate_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS translated_at      TIMESTAMPTZ;
ALTER TABLE feed_view
    ADD COLUMN IF NOT EXISTS title_pt   TEXT,
    ADD COLUMN IF NOT EXISTS summary_pt TEXT;`

func translationsTestDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	adminDSN := config.DatabaseURL()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var exists bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, testTranslationsDBName).Scan(&exists); err != nil {
		t.Fatalf("postgres unreachable at %s: %v", adminDSN, err)
	}
	if !exists {
		if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+testTranslationsDBName); err != nil {
			t.Fatalf("create test db: %v", err)
		}
	}
	u, _ := url.Parse(adminDSN)
	u.Path = "/" + testTranslationsDBName
	mdb, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()
	if err := migrations.Run(mdb, "up"); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	pool, err := Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, translationColumnsDDL); err != nil {
		t.Fatalf("translation columns: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE sources, items, clusters, feed_view RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

type seedItem struct {
	title, summary, status string
	titlePT                *string
	attempts               int
	projected              bool
}

func seedTranslationItems(t *testing.T, ctx context.Context, pool *pgxpool.Pool, items []seedItem) []int64 {
	t.Helper()
	var src int64
	if err := pool.QueryRow(ctx, `INSERT INTO sources (kind, name) VALUES ('rss', 'translate-test') RETURNING id`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		u := "https://example.com/" + it.title
		err := pool.QueryRow(ctx, `
INSERT INTO items (source_id, external_id, url, url_normalized, url_hash, title, summary, status, score, title_pt, translate_attempts)
VALUES ($1, $2, $3, $3, md5($3), $4, NULLIF($5, ''), $6, CASE WHEN $6 = 'raw' THEN NULL ELSE 7 END, $7, $8) RETURNING id`,
			src, it.title, u, it.title, it.summary, it.status, it.titlePT, it.attempts).Scan(&ids[i])
		if err != nil {
			t.Fatalf("seed %q: %v", it.title, err)
		}
		if it.projected {
			if _, err := pool.Exec(ctx, `
INSERT INTO feed_view (item_id, source_id, source_kind, source_name, title, url, summary, score, title_pt)
VALUES ($1, $2, 'rss', 'translate-test', $3, $4, $5, 7, $6)`, ids[i], src, it.title, u, it.summary, it.titlePT); err != nil {
				t.Fatalf("project %q: %v", it.title, err)
			}
		}
	}
	return ids
}

func strp(s string) *string { return &s }

func TestTranslationBackfillQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := translationsTestDB(t, ctx)
	repo := AIRepo{DB: pool}
	ids := seedTranslationItems(t, ctx, pool, []seedItem{
		{title: "pending-deduped", summary: "A summary.", status: "deduped", projected: true},                     // 0
		{title: "pending-scored", summary: "", status: "scored"},                                                  // 1: NULL summary
		{title: "pending-duplicate", summary: "Dup.", status: "duplicate"},                                        // 2
		{title: "raw-not-scored", summary: "", status: "raw"},                                                     // 3: never selected
		{title: "failed-scoring", summary: "", status: "failed"},                                                  // 4: never selected
		{title: "already-pt", summary: "Old.", status: "deduped", titlePT: strp("Já traduzido"), projected: true}, // 5
		{title: "gave-up", summary: "S.", status: "deduped", attempts: 3},                                         // 6
		{title: "one-try-left", summary: "S.", status: "deduped", attempts: 2},                                    // 7
	})

	rows, err := repo.PendingTranslations(ctx, 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]TranslateRow{}
	for _, r := range rows {
		got[r.ID] = r
	}
	for _, i := range []int{0, 1, 2, 7} {
		if _, ok := got[ids[i]]; !ok {
			t.Errorf("item %d should be pending", i)
		}
	}
	for _, i := range []int{3, 4, 5, 6} {
		if _, ok := got[ids[i]]; ok {
			t.Errorf("item %d must not be pending", i)
		}
	}
	if r := got[ids[1]]; r.Summary != "" {
		t.Errorf("NULL summary should scan as empty, got %q", r.Summary)
	}
	if r := got[ids[7]]; r.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", r.Attempts)
	}
	if lim, err := repo.PendingTranslations(ctx, 3, 2); err != nil || len(lim) != 2 || lim[0].ID != ids[0] {
		t.Fatalf("limit 2: %+v %v", lim, err)
	}

	st, err := repo.TranslationStatus(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 5 has title_pt but no pt_source (seeded directly): neither llm nor copy.
	if want := (domain.TranslationStatus{Remaining: 4, Failed: 1}); st != want {
		t.Fatalf("status = %+v, want %+v", st, want)
	}

	// Save on a projected item updates items and feed_view, never the originals.
	ok, err := repo.SaveTranslation(ctx, ids[0], domain.Translation{TitlePT: "Pendente", SummaryPT: "Um resumo."})
	if err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	var title, summary, titlePT, summaryPT, source string
	var at *time.Time
	if err := pool.QueryRow(ctx, `SELECT title, summary, title_pt, summary_pt, pt_source, translated_at FROM items WHERE id = $1`, ids[0]).
		Scan(&title, &summary, &titlePT, &summaryPT, &source, &at); err != nil {
		t.Fatal(err)
	}
	if title != "pending-deduped" || summary != "A summary." || titlePT != "Pendente" || summaryPT != "Um resumo." || source != "llm" || at == nil {
		t.Fatalf("items row: %q %q %q %q %q %v", title, summary, titlePT, summaryPT, source, at)
	}
	if err := pool.QueryRow(ctx, `SELECT title, summary, title_pt, summary_pt FROM feed_view WHERE item_id = $1`, ids[0]).
		Scan(&title, &summary, &titlePT, &summaryPT); err != nil {
		t.Fatal(err)
	}
	if title != "pending-deduped" || summary != "A summary." || titlePT != "Pendente" || summaryPT != "Um resumo." {
		t.Fatalf("feed_view row: %q %q %q %q", title, summary, titlePT, summaryPT)
	}

	// Idempotent: a second save of the same item (or of an item that already
	// had title_pt) changes nothing.
	if ok, err := repo.SaveTranslation(ctx, ids[0], domain.Translation{TitlePT: "Outro"}); err != nil || ok {
		t.Fatalf("second save: %v %v", ok, err)
	}
	if ok, err := repo.SaveTranslation(ctx, ids[5], domain.Translation{TitlePT: "Outro"}); err != nil || ok {
		t.Fatalf("save over existing title_pt: %v %v", ok, err)
	}
	if err := pool.QueryRow(ctx, `SELECT i.title_pt, f.title_pt FROM items i JOIN feed_view f ON f.item_id = i.id WHERE i.id = $1`, ids[5]).
		Scan(&titlePT, &summaryPT); err != nil || titlePT != "Já traduzido" || summaryPT != "Já traduzido" {
		t.Fatalf("existing translation was touched: %q %q %v", titlePT, summaryPT, err)
	}
	if _, err := repo.SaveTranslation(ctx, ids[1], domain.Translation{}); err == nil {
		t.Fatal("an empty title_pt must be rejected")
	}

	// Copy (already Portuguese) on an item not yet in feed_view.
	if ok, err := repo.SaveTranslation(ctx, ids[1], domain.Translation{TitlePT: "Já em PT", Copied: true}); err != nil || !ok {
		t.Fatalf("save copy: %v %v", ok, err)
	}

	// Attempts: the last one moves the item from remaining to failed; a bump
	// on a translated item is a no-op.
	if err := repo.BumpTranslateAttempts(ctx, ids[7]); err != nil {
		t.Fatal(err)
	}
	if err := repo.BumpTranslateAttempts(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT translate_attempts FROM items WHERE id = $1`, ids[0]).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("bump on a translated item: attempts=%d err=%v", attempts, err)
	}
	st, err = repo.TranslationStatus(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := (domain.TranslationStatus{Remaining: 1, Translated: 1, Copied: 1, Failed: 2}); st != want {
		t.Fatalf("status after = %+v, want %+v", st, want)
	}
	rows, err = repo.PendingTranslations(ctx, 3, 100)
	if err != nil || len(rows) != 1 || rows[0].ID != ids[2] {
		t.Fatalf("pending after: %+v %v", rows, err)
	}
}
