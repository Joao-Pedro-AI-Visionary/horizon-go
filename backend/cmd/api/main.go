// Command api serves the REST API of the dashboard (Phase 3).
//
// Environment (defaults from CLAUDE.md):
//
//	DATABASE_URL    full DSN; or PGUSER/PGPASSWORD/PGHOST/PGPORT/PGDATABASE
//	                (defaults: local dev container, see .env.example)
//	NATS_URL        nats://localhost:4230
//	API_ADDR        :8080
//	CORS_ORIGINS    http://localhost:5173,http://127.0.0.1:5173 (comma separated)
//	COLLECT_WINDOW  24h   how far back a triggered collection looks
//	SOURCE_TIMEOUT  30s   per-source fetch timeout
//
// Triggered collections publish to NATS items.raw; the worker persists, scores
// and dedups them.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // America/Sao_Paulo without a system zoneinfo

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/httpapi"
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/internal/digest"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ingest"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil && d > 0 {
		return d
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("api failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	pool, err := postgres.Connect(ctx, config.DatabaseURL())
	if err != nil {
		return err
	}
	defer pool.Close()
	bus, err := natsbus.Connect(ctx, natsbus.Config{URL: getenv("NATS_URL", "nats://localhost:4230")})
	if err != nil {
		return err
	}
	defer bus.Close()

	timeout := envDuration("SOURCE_TIMEOUT", app.DefaultSourceTimeout)
	read := postgres.ReadRepo{DB: pool}
	sources := postgres.SourceRepo{DB: pool}
	srv := &httpapi.Server{
		Store:   read,
		Sources: sources,
		Digests: &digest.Service{Store: read},
		Collector: &ingest.Pipeline{
			Sources:   sources,
			Collector: &app.Collector{Publisher: bus, Runs: postgres.FetchRunRepo{DB: pool}, Timeout: timeout, Log: log},
			Client:    &http.Client{Timeout: timeout},
			Log:       log,
		},
		ValidateSource: func(c domain.SourceConfig) error {
			_, err := source.Build(c, nil, source.BaseURLs{})
			return err
		},
		ValidateCron:   digest.ValidateCron,
		Checks:         []httpapi.Check{{Name: "postgres", Fn: read.Ping}, {Name: "nats", Fn: bus.Ping}},
		Window:         envDuration("COLLECT_WINDOW", 24*time.Hour),
		AllowedOrigins: strings.Split(getenv("CORS_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173"), ","),
		Log:            log,
	}
	hs := &http.Server{
		Addr:              getenv("API_ADDR", ":8080"),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      timeout + 2*time.Minute, // a triggered collection waits for its sources
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", hs.Addr, "cors", srv.AllowedOrigins)
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
