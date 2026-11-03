// Command worker runs the pipeline: enabled sources -> collector -> NATS
// items.raw -> persister -> items; then scorer (LLM rubric + embedding, N
// workers) -> items.scored -> deduper (pgvector + LLM) -> clusters, feed_view
// -> items.deduped.
//
//	worker --once [--seed] [--window 24h]   one cycle, then print the report
//	worker --once --skip-collect            score and dedup what is in the database
//	worker --once --ai=false                collect only (Phase 1 behaviour)
//	worker [--interval 30m]                 collect periodically and run every stage continuously
//
// DATABASE_URL, NATS_URL, OLLAMA_URL, LLM_MODEL, LLM_NUM_CTX and EMBED_MODEL override the
// local defaults from CLAUDE.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/llm/ollama"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/enrich"
	"github.com/jpdev/horizon-go/internal/ingest"
)

const (
	defaultDSN  = "postgres://horizon:horizon@localhost:5450/horizon?sslmode=disable"
	defaultNATS = "nats://localhost:4230"
)

type aiOptions struct {
	enabled     bool
	skipCollect bool
	workers     int
	dedupWindow time.Duration
	th          domain.Thresholds
	timeout     time.Duration
}

func main() {
	once := flag.Bool("once", false, "run a single collect+persist cycle and exit")
	seed := flag.Bool("seed", false, "insert the default sources (HN, r/golang, Go blog) if missing")
	window := flag.Duration("window", 24*time.Hour, "only keep items published within this window")
	timeout := flag.Duration("timeout", app.DefaultSourceTimeout, "per-source fetch timeout")
	interval := flag.Duration("interval", 30*time.Minute, "collect interval in continuous mode")
	var o aiOptions
	flag.BoolVar(&o.enabled, "ai", true, "run the scoring and dedup stages")
	flag.BoolVar(&o.skipCollect, "skip-collect", false, "with --once: do not collect, only process items already in the database")
	flag.IntVar(&o.workers, "score-workers", envInt("SCORE_WORKERS", enrich.DefaultWorkers), "scorer worker pool size (env SCORE_WORKERS)")
	flag.DurationVar(&o.dedupWindow, "dedup-window", enrich.DefaultWindow, "neighbour search window around the item time")
	flag.Float64Var(&o.th.Duplicate, "dup-threshold", domain.DefaultThresholds.Duplicate, "cosine similarity for a direct duplicate")
	flag.Float64Var(&o.th.Ambiguous, "ambiguous-threshold", domain.DefaultThresholds.Ambiguous, "cosine similarity from which the LLM decides")
	flag.DurationVar(&o.timeout, "ai-timeout", 2*time.Hour, "with --once: bound for the scoring and dedup stages")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log, *once, *seed, *window, *timeout, *interval, o); err != nil {
		log.Error("worker failed", "err", err)
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

type aiStages struct {
	scorer     *enrich.Scorer
	deduper    *enrich.Deduper
	rawCons    jetstream.Consumer
	scoredCons jetstream.Consumer
}

func newAIStages(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, bus *natsbus.Bus, o aiOptions) (*aiStages, error) {
	oc := ollama.New(ollama.Config{
		URL:        getenv("OLLAMA_URL", ollama.DefaultURL),
		ChatModel:  getenv("LLM_MODEL", ollama.DefaultChatModel),
		EmbedModel: getenv("EMBED_MODEL", ollama.DefaultEmbedModel),
		NumCtx:     envInt("LLM_NUM_CTX", ollama.DefaultNumCtx),
	})
	an := &app.Analyzer{LLM: oc.LLM(), Embedder: oc.Embedder(), Cache: postgres.AICache{DB: pool}}
	repo := postgres.AIRepo{DB: pool}
	rawCons, err := bus.Consumer(ctx, enrich.ScorerDurable, bus.RawSubject(), enrich.AckWait)
	if err != nil {
		return nil, err
	}
	scoredCons, err := bus.Consumer(ctx, enrich.DeduperDurable, bus.ScoredSubject(), enrich.AckWait)
	if err != nil {
		return nil, err
	}
	log.Info("ai stages ready", "llm", an.LLM.Model(), "embedder", an.Embedder.Model(), "workers", o.workers,
		"dup", o.th.Duplicate, "ambiguous", o.th.Ambiguous, "window", o.dedupWindow)
	return &aiStages{
		scorer:     &enrich.Scorer{Repo: repo, Analyzer: an, Bus: bus, Workers: o.workers, Log: log},
		deduper:    &enrich.Deduper{Repo: repo, Analyzer: an, Bus: bus, Thresholds: o.th, Window: o.dedupWindow, Log: log},
		rawCons:    rawCons,
		scoredCons: scoredCons,
	}, nil
}

func run(ctx context.Context, log *slog.Logger, once, seed bool, window, timeout, interval time.Duration, o aiOptions) error {
	pool, err := postgres.Connect(ctx, getenv("DATABASE_URL", defaultDSN))
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	bus, err := natsbus.Connect(ctx, natsbus.Config{URL: getenv("NATS_URL", defaultNATS)})
	if err != nil {
		return err
	}
	defer bus.Close()

	sources := postgres.SourceRepo{DB: pool}
	if seed {
		for _, s := range ingest.DefaultSources() {
			row, err := sources.Ensure(ctx, s)
			if err != nil {
				return fmt.Errorf("seed %s: %w", s.Name, err)
			}
			log.Info("source ready", "id", row.ID, "kind", row.Kind, "name", row.Name, "enabled", row.Enabled)
		}
	}

	persister, err := bus.NewPersister(ctx, natsbus.DefaultDurable, postgres.ItemRepo{DB: pool}, log)
	if err != nil {
		return err
	}
	p := &ingest.Pipeline{
		Sources:   sources,
		Collector: &app.Collector{Publisher: bus, Runs: postgres.FetchRunRepo{DB: pool}, Timeout: timeout, Log: log},
		Persister: persister,
		Client:    &http.Client{Timeout: timeout},
		Log:       log,
	}

	var ai *aiStages
	if o.enabled {
		if ai, err = newAIStages(ctx, log, pool, bus, o); err != nil {
			return err
		}
	}

	if once {
		if !o.skipCollect {
			cctx, cancel := context.WithTimeout(ctx, timeout+2*time.Minute)
			res, err := p.Once(cctx, time.Now().Add(-window))
			cancel()
			printReport(ctx, pool, res)
			if err != nil {
				return err
			}
		}
		if ai == nil {
			return nil
		}
		actx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		start := time.Now()
		serr := ai.scorer.Drain(actx, ai.rawCons)
		scoredIn := time.Since(start)
		derr := ai.deduper.Drain(actx, ai.scoredCons)
		printAIReport(ctx, pool, ai, scoredIn, time.Since(start))
		return errors.Join(serr, derr)
	}

	go func() {
		if err := persister.Run(ctx); err != nil {
			log.Error("persister stopped", "err", err)
		}
	}()
	if ai != nil {
		go func() {
			if err := ai.scorer.Run(ctx, ai.rawCons); err != nil {
				log.Error("scorer stopped", "err", err)
			}
		}()
		go func() {
			if err := ai.deduper.Run(ctx, ai.scoredCons); err != nil {
				log.Error("deduper stopped", "err", err)
			}
		}()
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		rep, err := p.Collect(ctx, time.Now().Add(-window))
		if err != nil {
			log.Error("collect failed", "err", err)
		}
		log.Info("collect done", "run", rep.RunID, "fetched", rep.Fetched, "published", rep.Published, "duplicates", rep.Duplicates)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func printReport(ctx context.Context, pool *pgxpool.Pool, res ingest.Result) {
	r := res.Report
	fmt.Printf("\nrun %s\n", r.RunID)
	fmt.Printf("%-14s %-6s %6s  %s\n", "SOURCE", "STATUS", "ITEMS", "ERROR")
	for _, s := range r.Sources {
		fmt.Printf("%-14s %-6s %6d  %s\n", s.SourceName, s.Status, s.ItemCount, s.Error)
	}
	fmt.Printf("fetched=%d invalid=%d duplicates=%d published=%d persisted=%d\n",
		r.Fetched, r.Invalid, r.Duplicates, r.Published, res.Persisted)

	rows, err := pool.Query(ctx, `SELECT s.name, count(i.id) FROM sources s LEFT JOIN items i ON i.source_id = s.id GROUP BY s.id, s.name ORDER BY s.id`)
	if err != nil {
		fmt.Println("count items:", err)
		return
	}
	defer rows.Close()
	fmt.Println("\nitems in database by source:")
	total := 0
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err == nil {
			fmt.Printf("  %-14s %d\n", name, n)
			total += n
		}
	}
	fmt.Printf("  %-14s %d\n", "TOTAL", total)
}

// printQuery prints every row of q, columns separated by " | ".
func printQuery(ctx context.Context, pool *pgxpool.Pool, title, q string) {
	fmt.Println("\n" + title)
	rows, err := pool.Query(ctx, q)
	if err != nil {
		fmt.Println("  error:", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			fmt.Println("  error:", err)
			return
		}
		cols := make([]string, len(vals))
		for i, v := range vals {
			cols[i] = fmt.Sprint(v)
		}
		fmt.Println("  " + strings.Join(cols, " | "))
	}
}

func printAIReport(ctx context.Context, pool *pgxpool.Pool, ai *aiStages, scoredIn, total time.Duration) {
	st := &ai.scorer.Stats
	fmt.Printf("\nscorer: scored=%d skipped=%d failed=%d in %s\n",
		st.Scored.Load(), st.Skipped.Load(), st.Failed.Load(), scoredIn.Round(time.Second))
	d := ai.deduper.Stats
	fmt.Printf("deduper: processed=%d new_clusters=%d joined=%d direct=%d llm_asked=%d llm_confirmed=%d skipped=%d failed=%d (total %s)\n",
		d.Processed, d.NewClusters, d.Joined, d.Direct, d.AskedLLM, d.Confirmed, d.Skipped, d.Failed, total.Round(time.Second))

	printQuery(ctx, pool, "items by status (status | n):",
		`SELECT status, count(*) FROM items GROUP BY status ORDER BY status`)
	printQuery(ctx, pool, "score distribution (bucket | n):", `
SELECT CASE WHEN score >= 9 THEN '9-10' WHEN score >= 7 THEN '7-8' WHEN score >= 5 THEN '5-6' WHEN score >= 3 THEN '3-4' ELSE '0-2' END, count(*)
FROM items WHERE score IS NOT NULL GROUP BY 1 ORDER BY 1 DESC`)
	printQuery(ctx, pool, "score stats (n | min | avg | max):",
		`SELECT count(score), min(score), round(avg(score)::numeric, 2), max(score) FROM items`)
	printQuery(ctx, pool, "clusters (total | with duplicates | items in those | feed_view rows):", `
SELECT (SELECT count(*) FROM clusters), (SELECT count(*) FROM clusters WHERE item_count > 1),
       (SELECT COALESCE(sum(item_count), 0) FROM clusters WHERE item_count > 1), (SELECT count(*) FROM feed_view)`)
	printQuery(ctx, pool, "multi-item clusters (id | size | primary score | members):", `
SELECT c.id, c.item_count, p.score, string_agg(left(i.title, 60), ' || ' ORDER BY i.score DESC)
FROM clusters c JOIN items p ON p.id = c.representative_item_id JOIN items i ON i.cluster_id = c.id
WHERE c.item_count > 1 GROUP BY c.id, c.item_count, p.score ORDER BY c.item_count DESC, c.id`)
	printQuery(ctx, pool, "top feed_view (score | dups | source | title):",
		`SELECT score, duplicate_count, source_kind, left(title, 70) FROM feed_view ORDER BY score DESC, published_at DESC LIMIT 10`)
	printQuery(ctx, pool, "ai_cache (kind | rows):",
		`SELECT kind, count(*) FROM ai_cache GROUP BY kind ORDER BY kind`)
}
