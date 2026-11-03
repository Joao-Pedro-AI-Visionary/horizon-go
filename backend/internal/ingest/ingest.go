// Package ingest wires the Phase 1 pipeline: configured sources -> collector
// -> NATS items.raw -> durable persister -> items.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// Pipeline holds the dependencies of one ingestion cycle.
type Pipeline struct {
	Sources   ports.SourceRepo
	Collector *app.Collector
	Persister *natsbus.Persister
	Client    *http.Client
	BaseURLs  source.BaseURLs
	Log       *slog.Logger
}

// Result is the outcome of Once.
type Result struct {
	Report    app.Report
	Persisted int
}

// brokenSource reports a configuration error through the normal fetch_runs path.
type brokenSource struct {
	name string
	err  error
}

func (b brokenSource) Name() string { return b.name }
func (b brokenSource) Fetch(context.Context, time.Time) ([]domain.Item, error) {
	return nil, b.err
}

// Jobs builds one job per enabled source.
func (p *Pipeline) Jobs(ctx context.Context) ([]app.Job, error) {
	rows, err := p.Sources.List(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	jobs := make([]app.Job, 0, len(rows))
	for _, row := range rows {
		s, err := source.Build(row, p.Client, p.BaseURLs)
		if err != nil {
			s = brokenSource{name: row.Name, err: err}
		}
		jobs = append(jobs, app.Job{SourceID: row.ID, Source: s})
	}
	return jobs, nil
}

// Collect runs the collector over every enabled source.
func (p *Pipeline) Collect(ctx context.Context, since time.Time) (app.Report, error) {
	jobs, err := p.Jobs(ctx)
	if err != nil {
		return app.Report{}, err
	}
	return p.Collector.Run(ctx, jobs, since)
}

// Once collects, then drains the consumer so every published item is in
// Postgres when it returns.
func (p *Pipeline) Once(ctx context.Context, since time.Time) (Result, error) {
	rep, err := p.Collect(ctx, since)
	if err != nil {
		return Result{Report: rep}, err
	}
	n, err := p.Persister.Drain(ctx)
	return Result{Report: rep, Persisted: n}, err
}

// DefaultSources are seeded by `worker --seed`.
func DefaultSources() []domain.SourceConfig {
	return []domain.SourceConfig{
		{Kind: domain.KindHackerNews, Name: "Hacker News", Enabled: true, Config: json.RawMessage(`{"min_score":100,"limit":30}`)},
		{Kind: domain.KindReddit, Name: "r/golang", Enabled: true, Config: json.RawMessage(`{"subreddit":"golang","sort":"hot","limit":25}`)},
		{Kind: domain.KindRSS, Name: "The Go Blog", Enabled: true, Config: json.RawMessage(`{"url":"https://go.dev/blog/feed.atom"}`)},
	}
}
