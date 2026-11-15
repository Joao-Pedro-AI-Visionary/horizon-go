package enrich

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
	"github.com/jpdev/horizon-go/internal/retry"
)

// brokenTranslator scores normally but never returns a valid translation
// (test-only).
type brokenTranslator struct {
	mu         sync.Mutex
	transCalls int
}

func (b *brokenTranslator) Model() string { return "broken-translator" }
func (b *brokenTranslator) Chat(_ context.Context, req ports.ChatRequest) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.Contains(req.System, "news translator") {
		b.transCalls++
		return "not json", nil
	}
	if strings.Contains(req.User, "Nova versão") {
		return `{"score": 8, "reason": "r", "summary": "O Go lançou uma nova versão com melhorias de desempenho no compilador.", "tags": ["go"]}`, nil
	}
	return `{"score": 7, "reason": "r", "summary": "Go 1.28 ships with a faster compiler and new tools.", "tags": ["go"]}`, nil
}

// Portuguese text is copied without a model call; a failed translation keeps
// the score; SaveTranslation fills an already-projected feed_view row once.
func TestTranslateStepCopiesPortugueseAndIsBestEffort(t *testing.T) {
	if os.Getenv("HORIZON_SKIP_INTEGRATION") == "1" {
		t.Skip("HORIZON_SKIP_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	bus := testBus(t, ctx)

	src, err := postgres.SourceRepo{DB: pool}.Create(ctx, domain.SourceConfig{Kind: domain.KindRSS, Name: "pt feed", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for key, title := range map[string]string{
		"pt": "Nova versão do Go é lançada com melhorias no compilador",
		"en": "Go 1.28 ships with a faster compiler",
	} {
		it := domain.Item{SourceID: src.ID, ExternalID: key, URL: "https://example.test/" + key, Title: title,
			Content: "content " + key, PublishedAt: now, FetchedAt: now}
		if err := it.Prepare(); err != nil {
			t.Fatal(err)
		}
		if _, err := (postgres.ItemRepo{DB: pool}).Upsert(ctx, it); err != nil {
			t.Fatal(err)
		}
	}

	model := &brokenTranslator{}
	an := &app.Analyzer{LLM: model, Cache: postgres.AICache{DB: pool}, Retry: retry.Policy{Attempts: 2, Base: time.Millisecond}}
	ai := postgres.AIRepo{DB: pool}
	scorer := &Scorer{Repo: ai, Analyzer: an, Bus: bus, Workers: 1}
	if err := scorer.Drain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if scorer.Stats.Scored.Load() != 2 || scorer.Stats.Failed.Load() != 0 {
		t.Fatalf("scored=%d failed=%d, want 2/0", scorer.Stats.Scored.Load(), scorer.Stats.Failed.Load())
	}
	if scorer.Stats.Translated.Load() != 1 || scorer.Stats.TranslateFailed.Load() != 1 || model.transCalls != 2 {
		t.Fatalf("translated=%d translateFailed=%d translation calls=%d, want 1/1/2 (retry on the en item only)",
			scorer.Stats.Translated.Load(), scorer.Stats.TranslateFailed.Load(), model.transCalls)
	}

	type row struct {
		status, title, summary string
		titlePT, summaryPT     *string
		ptSource               *string
		score                  *float64
		attempts               int
	}
	load := func(key string) row {
		t.Helper()
		var r row
		if err := pool.QueryRow(ctx, `
SELECT status, title, COALESCE(summary, ''), title_pt, summary_pt, pt_source, score, translate_attempts
FROM items WHERE external_id = $1`, key).Scan(&r.status, &r.title, &r.summary, &r.titlePT, &r.summaryPT, &r.ptSource, &r.score, &r.attempts); err != nil {
			t.Fatal(err)
		}
		return r
	}
	pt := load("pt")
	if pt.titlePT == nil || *pt.titlePT != pt.title || pt.summaryPT == nil || *pt.summaryPT != pt.summary ||
		pt.ptSource == nil || *pt.ptSource != domain.PTSourceCopy {
		t.Errorf("pt item not copied: %+v", pt)
	}
	en := load("en")
	if en.score == nil || en.status != "scored" || en.titlePT != nil || en.summaryPT != nil || en.ptSource != nil || en.attempts != 0 {
		t.Errorf("en item after failed translation: %+v (want score kept, no pt copy, attempts untouched)", en)
	}

	// Project both (no embedder: one cluster each); ApplyCluster copies the pt columns.
	ded := &Deduper{Repo: ai, Analyzer: an, Bus: bus}
	if err := ded.Drain(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var fvPT *string
	if err := pool.QueryRow(ctx, `SELECT f.title_pt FROM feed_view f JOIN items i ON i.id = f.item_id WHERE i.external_id = 'pt'`).Scan(&fvPT); err != nil {
		t.Fatal(err)
	}
	if fvPT == nil || *fvPT != pt.title {
		t.Errorf("feed_view title_pt of pt item = %v", fvPT)
	}

	// Backfill path: SaveTranslation updates items and the projected row once.
	var enID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM items WHERE external_id = 'en'`).Scan(&enID); err != nil {
		t.Fatal(err)
	}
	tr := domain.Translation{TitlePT: "Go 1.28 sai com um compilador mais rápido", SummaryPT: "O Go 1.28 traz um compilador mais rápido."}
	if ok, err := ai.SaveTranslation(ctx, enID, tr); err != nil || !ok {
		t.Fatalf("SaveTranslation = %v, %v; want true", ok, err)
	}
	if ok, err := ai.SaveTranslation(ctx, enID, domain.Translation{TitlePT: "outra", SummaryPT: "outra"}); err != nil || ok {
		t.Fatalf("second SaveTranslation = %v, %v; want false (no overwrite)", ok, err)
	}
	en = load("en")
	if en.title != "Go 1.28 ships with a faster compiler" || en.titlePT == nil || *en.titlePT != tr.TitlePT ||
		en.ptSource == nil || *en.ptSource != domain.PTSourceLLM {
		t.Errorf("en item after SaveTranslation: %+v", en)
	}

	// Read model: feed scan and search see the pt columns.
	page, err := postgres.ReadRepo{DB: pool}.Feed(ctx, domain.FeedQuery{Sort: "recent", Page: 1, PageSize: 20, Q: "compilador mais rápido"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].TitlePT != tr.TitlePT || page.Items[0].SummaryPT != tr.SummaryPT ||
		page.Items[0].DisplayTitle() != tr.TitlePT || page.Items[0].Title != en.title {
		t.Fatalf("feed search by pt text = %+v", page.Items)
	}
	detail, err := postgres.ReadRepo{DB: pool}.Item(ctx, enID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TitlePT != tr.TitlePT || detail.SummaryPT != tr.SummaryPT || detail.DisplaySummary() != tr.SummaryPT {
		t.Errorf("item detail pt = %q / %q", detail.TitlePT, detail.SummaryPT)
	}
}
