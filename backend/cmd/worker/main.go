// Command worker runs the ingestion pipeline: enabled sources -> collector ->
// NATS items.raw -> durable consumer -> items.
//
//	worker --once [--seed] [--window 24h]   one cycle, then print the report
//	worker [--interval 30m]                 collect periodically and consume continuously
//
// DATABASE_URL and NATS_URL override the local defaults from CLAUDE.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/ingest"
)

const (
	defaultDSN  = "postgres://horizon:horizon@localhost:5450/horizon?sslmode=disable"
	defaultNATS = "nats://localhost:4230"
)

func main() {
	once := flag.Bool("once", false, "run a single collect+persist cycle and exit")
	seed := flag.Bool("seed", false, "insert the default sources (HN, r/golang, Go blog) if missing")
	window := flag.Duration("window", 24*time.Hour, "only keep items published within this window")
	timeout := flag.Duration("timeout", app.DefaultSourceTimeout, "per-source fetch timeout")
	interval := flag.Duration("interval", 30*time.Minute, "collect interval in continuous mode")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log, *once, *seed, *window, *timeout, *interval); err != nil {
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

func run(ctx context.Context, log *slog.Logger, once, seed bool, window, timeout, interval time.Duration) error {
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

	if once {
		cctx, cancel := context.WithTimeout(ctx, timeout+2*time.Minute)
		defer cancel()
		res, err := p.Once(cctx, time.Now().Add(-window))
		printReport(ctx, pool, res)
		return err
	}

	go func() {
		if err := persister.Run(ctx); err != nil {
			log.Error("persister stopped", "err", err)
		}
	}()
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
