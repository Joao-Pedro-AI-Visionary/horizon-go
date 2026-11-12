package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// DefaultSourceTimeout bounds each source fetch.
const DefaultSourceTimeout = 30 * time.Second

// Job pairs a configured source row with its adapter.
type Job struct {
	SourceID int64
	Source   ports.Source
}

// Collector fans out one goroutine per source, fans results into a channel,
// normalizes and dedups items by URL, publishes them and records a fetch_run
// per source.
type Collector struct {
	Publisher ports.ItemPublisher
	Runs      ports.FetchRunRepo
	Timeout   time.Duration
	Log       *slog.Logger
}

// Report summarizes one collector run.
type Report struct {
	RunID      string
	Sources    []domain.FetchRun
	Fetched    int // items returned by all sources
	Invalid    int // dropped: URL could not be normalized
	Duplicates int // dropped: same normalized URL already seen in this run
	Published  int
}

type result struct {
	run   domain.FetchRun
	items []domain.Item
}

// Run executes every job concurrently. A failing or slow source is recorded
// as an error in fetch_runs and does not affect the others; a source that
// returns a domain.SkippedError is recorded as skipped.
func (c *Collector) Run(ctx context.Context, jobs []Job, since time.Time) (Report, error) {
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultSourceTimeout
	}
	rep := Report{RunID: newRunID()}

	results := make(chan result, len(jobs))
	g, gctx := errgroup.WithContext(ctx)
	for _, job := range jobs {
		g.Go(func() error {
			results <- c.fetchOne(gctx, job, since, timeout, rep.RunID)
			return nil
		})
	}
	go func() {
		_ = g.Wait()
		close(results)
	}()

	var (
		errs  []error
		order []string
		byKey = map[string]domain.Item{}
	)
	for res := range results {
		if _, err := c.Runs.Insert(ctx, res.run); err != nil {
			errs = append(errs, fmt.Errorf("save fetch_run for source %d: %w", res.run.SourceID, err))
		}
		rep.Sources = append(rep.Sources, res.run)
		log.Info("source fetched", "source", res.run.SourceName, "status", res.run.Status,
			"items", res.run.ItemCount, "error", res.run.Error)

		for _, it := range res.items {
			rep.Fetched++
			if err := it.Prepare(); err != nil {
				rep.Invalid++
				continue
			}
			prev, seen := byKey[it.URLHash]
			if !seen {
				order = append(order, it.URLHash)
				byKey[it.URLHash] = it
				continue
			}
			rep.Duplicates++
			if len(it.Content) > len(prev.Content) {
				byKey[it.URLHash] = it
			}
		}
	}

	unique := make([]domain.Item, 0, len(order))
	for _, k := range order {
		unique = append(unique, byKey[k])
	}
	if len(unique) > 0 {
		if err := c.Publisher.PublishRaw(ctx, rep.RunID, unique); err != nil {
			errs = append(errs, err)
		} else {
			rep.Published = len(unique)
		}
	}
	return rep, errors.Join(errs...)
}

func (c *Collector) fetchOne(ctx context.Context, job Job, since time.Time, timeout time.Duration, runID string) (res result) {
	res.run = domain.FetchRun{RunID: runID, SourceID: job.SourceID, SourceName: job.Source.Name(), StartedAt: time.Now().UTC()}
	defer func() {
		if r := recover(); r != nil {
			res.items = nil
			res.run.Status, res.run.Error, res.run.ItemCount = domain.FetchError, fmt.Sprintf("panic: %v", r), 0
		}
		res.run.FinishedAt = time.Now().UTC()
	}()

	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	items, err := job.Source.Fetch(sctx, since)
	var skipped *domain.SkippedError
	if errors.As(err, &skipped) {
		res.run.Status, res.run.Error = domain.FetchSkipped, skipped.Error()
		return res
	}
	if err != nil {
		res.run.Status, res.run.Error = domain.FetchError, err.Error()
		return res
	}
	res.run.Status, res.run.ItemCount, res.items = domain.FetchOK, len(items), items
	return res
}

func newRunID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}
