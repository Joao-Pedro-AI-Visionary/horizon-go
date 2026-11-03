// Package enrich wires the Phase 2 stages:
//
//	items.raw / items without score -> scorer (N workers: LLM rubric + embedding)
//	  -> items.scored -> deduper (pgvector neighbours, LLM in the ambiguous band)
//	  -> clusters + feed_view projection -> items.deduped
package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
)

// Durable consumer names.
const (
	ScorerDurable  = "items-scorer"
	DeduperDurable = "items-deduper"
)

// Defaults of the stages.
const (
	DefaultWorkers     = 2
	DefaultMaxAttempts = 5
	DefaultWindow      = 72 * time.Hour
	DefaultNeighbors   = 10
	// AckWait is the redelivery timer; handlers renew it with InProgress
	// every AckWait/3 while a slow model call runs.
	AckWait = 5 * time.Minute
	// DefaultBackfillEvery re-queues, in continuous mode, work whose message
	// was lost or terminated.
	DefaultBackfillEvery = 10 * time.Minute
	nakDelay             = 5 * time.Second
)

// heartbeat keeps msg in progress until the returned stop is called.
func heartbeat(msg jetstream.Msg) (stop func()) {
	if msg == nil {
		return func() {}
	}
	_ = msg.InProgress()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(AckWait / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = msg.InProgress()
			}
		}
	}()
	return func() { close(done) }
}

// every calls fn at each tick until ctx is done.
func every(ctx context.Context, d time.Duration, fn func()) {
	if d <= 0 {
		d = DefaultBackfillEvery
	}
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

func settle(msg jetstream.Msg, err error, log *slog.Logger) {
	if msg == nil {
		return
	}
	if err == nil {
		_ = msg.Ack()
		return
	}
	if md, mdErr := msg.Metadata(); mdErr == nil && md.NumDelivered >= natsbus.MaxDeliver {
		log.Warn("giving up on message, the periodic backfill retries from the database", "subject", msg.Subject(), "err", err)
		_ = msg.Term()
		return
	}
	_ = msg.NakWithDelay(nakDelay)
}

// ScoreStats counts scorer outcomes.
type ScoreStats struct {
	Scored, Skipped, Failed atomic.Int64
}

// Scorer is the scoring stage.
type Scorer struct {
	Repo        postgres.AIRepo
	Analyzer    *app.Analyzer
	Bus         *natsbus.Bus
	Workers     int
	MaxAttempts int
	// BackfillEvery is the period of the database sweep in Run.
	BackfillEvery time.Duration
	Log           *slog.Logger

	Stats ScoreStats
}

func (s *Scorer) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Scorer) workers() int {
	if s.Workers <= 0 {
		return DefaultWorkers
	}
	return s.Workers
}

func (s *Scorer) maxAttempts() int {
	if s.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return s.MaxAttempts
}

// ScoreItem analyzes and embeds one item, then publishes items.scored. It
// returns false when the item already had a score or was given up on.
func (s *Scorer) ScoreItem(ctx context.Context, id int64) (bool, error) {
	row, err := s.Repo.LoadScoreRow(ctx, id)
	if err != nil {
		return false, err
	}
	if row.Scored || row.Failed {
		return false, nil
	}
	a, err := s.Analyzer.Analyze(ctx, app.ScoreInput{
		Title: row.Title, URL: row.URL, Source: row.SourceKind, Author: row.Author, Content: row.Content,
	})
	if err != nil {
		return false, err
	}
	emb, err := s.Analyzer.Embed(ctx, app.EmbeddingText(row.Title, a.Summary))
	if err != nil {
		return false, err
	}
	saved, err := s.Repo.SaveScore(ctx, id, a, emb)
	if err != nil || !saved {
		return false, err
	}
	if err := s.Bus.PublishScored(ctx, id); err != nil {
		// The item is scored; the deduper backfill picks it up anyway.
		s.log().Warn("publish scored failed", "item", id, "err", err)
	}
	s.log().Info("scored", "item", id, "score", a.Score, "tags", a.Tags)
	return true, nil
}

type scoreJob struct {
	id  int64
	msg jetstream.Msg
}

func (s *Scorer) process(ctx context.Context, j scoreJob) {
	stop := heartbeat(j.msg)
	ok, err := s.ScoreItem(ctx, j.id)
	stop()
	switch {
	case err != nil:
		s.Stats.Failed.Add(1)
		s.log().Error("score failed", "item", j.id, "err", err)
		// Only output the model keeps getting wrong for this item spends an
		// attempt; outages, cancellation and config errors do not.
		counts := errors.Is(err, app.ErrInvalidOutput)
		if rerr := s.Repo.RecordScoreFailure(context.WithoutCancel(ctx), j.id, err, counts, s.maxAttempts()); rerr != nil {
			s.log().Error("record failure", "item", j.id, "err", rerr)
		}
	case ok:
		s.Stats.Scored.Add(1)
	default:
		s.Stats.Skipped.Add(1)
	}
	settle(j.msg, err, s.log())
}

// startPool runs the workers. The jobs channel is never closed (Consume
// callbacks may outlive Stop); wait stops the workers once the current jobs
// are done. Senders must give up on ctx.Done.
func (s *Scorer) startPool(ctx context.Context) (chan<- scoreJob, func()) {
	jobs := make(chan scoreJob)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range s.workers() {
		wg.Go(func() {
			for {
				select {
				case j := <-jobs:
					s.process(ctx, j)
				case <-done:
					return
				}
			}
		})
	}
	return jobs, func() { close(done); wg.Wait() }
}

// rawHandler resolves an items.raw message to its row and queues it.
func (s *Scorer) rawHandler(ctx context.Context, jobs chan<- scoreJob) func(jetstream.Msg) {
	return func(msg jetstream.Msg) {
		if ctx.Err() != nil {
			_ = msg.Nak()
			return
		}
		var it domain.Item
		if err := json.Unmarshal(msg.Data(), &it); err != nil || it.URLHash == "" {
			_ = msg.Term()
			return
		}
		id, err := s.Repo.IDByURLHash(ctx, it.URLHash)
		if err != nil {
			// Not persisted yet (or dropped as invalid): retry later.
			settle(msg, err, s.log())
			return
		}
		select {
		case jobs <- scoreJob{id: id, msg: msg}:
		case <-ctx.Done():
			_ = msg.Nak()
		}
	}
}

// Backfill queues every item that still has no score.
func (s *Scorer) backfill(ctx context.Context, jobs chan<- scoreJob) error {
	ids, err := s.Repo.PendingScore(ctx, s.maxAttempts(), 100_000)
	if err != nil {
		return err
	}
	for _, id := range ids {
		select {
		case jobs <- scoreJob{id: id}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Drain consumes items.raw until empty, then scores what is left in the
// database. Used by `worker --once`.
func (s *Scorer) Drain(ctx context.Context, cons jetstream.Consumer) error {
	jobs, wait := s.startPool(ctx)
	var err error
	if cons != nil {
		_, err = natsbus.Drain(ctx, cons, s.workers(), s.rawHandler(ctx, jobs))
	}
	if err == nil {
		err = s.backfill(ctx, jobs)
	}
	wait()
	return err
}

// Run consumes items.raw until ctx is cancelled and sweeps the database for
// unscored items at start and every BackfillEvery.
func (s *Scorer) Run(ctx context.Context, cons jetstream.Consumer) error {
	jobs, wait := s.startPool(ctx)
	defer wait()
	cc, err := cons.Consume(s.rawHandler(ctx, jobs), jetstream.PullMaxMessages(s.workers()))
	if err != nil {
		return err
	}
	sweep := func() {
		if err := s.backfill(ctx, jobs); err != nil && ctx.Err() == nil {
			s.log().Error("score backfill", "err", err)
		}
	}
	sweep()
	every(ctx, s.BackfillEvery, sweep)
	cc.Stop()
	<-cc.Closed()
	return nil
}

// Decision is how the neighbours of an item were judged.
type Decision struct {
	Direct    []int64 // similarity >= duplicate threshold
	Ambiguous []int64 // in the band, sent to the LLM
	Confirmed []int64 // ambiguous ones the LLM called the same event
}

// Matched lists the items the new one duplicates.
func (d Decision) Matched() []int64 {
	out := append(append([]int64{}, d.Direct...), d.Confirmed...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func candidate(r postgres.DedupRow) app.DedupCandidate {
	return app.DedupCandidate{ItemID: r.ID, Title: r.Title, Summary: r.Summary, Tags: r.Tags, Score: r.Score}
}

// SelectMatches applies the thresholds to the neighbours of target and asks
// the LLM only about the ambiguous band.
func SelectMatches(ctx context.Context, a *app.Analyzer, th domain.Thresholds, target postgres.DedupRow, neighbors []postgres.DedupRow) (Decision, error) {
	var d Decision
	var amb []app.DedupCandidate
	for _, n := range neighbors {
		switch th.Classify(n.Similarity) {
		case domain.Duplicate:
			d.Direct = append(d.Direct, n.ID)
		case domain.Ambiguous:
			d.Ambiguous = append(d.Ambiguous, n.ID)
			amb = append(amb, candidate(n))
		}
	}
	if len(amb) == 0 {
		return d, nil
	}
	same, err := a.SameEvent(ctx, candidate(target), amb)
	if err != nil {
		return d, err
	}
	d.Confirmed = same
	return d, nil
}

// DedupStats counts deduper outcomes.
type DedupStats struct {
	Processed, NewClusters, Joined, Direct, AskedLLM, Confirmed, Skipped, Failed int
}

// Deduper is the dedup stage. Items are handled one at a time.
//
// Run a single deduper per database: the neighbour search and the LLM call
// happen outside the advisory lock (only cluster writes take it), so two
// processes deduping copies of the same story at the same instant would not
// see each other and could create two clusters.
type Deduper struct {
	Repo       postgres.AIRepo
	Analyzer   *app.Analyzer
	Bus        *natsbus.Bus
	Thresholds domain.Thresholds
	Window     time.Duration
	Neighbors  int
	// BackfillEvery is the period of the database sweep in Run.
	BackfillEvery time.Duration
	Log           *slog.Logger

	mu    sync.Mutex
	Stats DedupStats
}

func (d *Deduper) log() *slog.Logger {
	if d.Log == nil {
		return slog.Default()
	}
	return d.Log
}

// DedupItem clusters one scored item.
func (d *Deduper) DedupItem(ctx context.Context, id int64) (postgres.ClusterResult, error) {
	th := d.Thresholds
	if th == (domain.Thresholds{}) {
		th = domain.DefaultThresholds
	}
	window, k := d.Window, d.Neighbors
	if window <= 0 {
		window = DefaultWindow
	}
	if k <= 0 {
		k = DefaultNeighbors
	}
	row, err := d.Repo.LoadDedupRow(ctx, id)
	if err != nil {
		return postgres.ClusterResult{}, err
	}
	if row.Status != "scored" || len(row.Embedding) == 0 {
		return postgres.ClusterResult{Skipped: true}, nil
	}
	ns, err := d.Repo.Neighbors(ctx, id, row.Embedding, row.PublishedAt, window, k)
	if err != nil {
		return postgres.ClusterResult{}, err
	}
	dec, err := SelectMatches(ctx, d.Analyzer, th, row, ns)
	if err != nil {
		return postgres.ClusterResult{}, err
	}
	res, err := d.Repo.ApplyCluster(ctx, id, dec.Matched())
	if err != nil {
		return res, err
	}
	if res.Skipped {
		return res, nil
	}
	d.Stats.Direct += len(dec.Direct)
	d.Stats.AskedLLM += len(dec.Ambiguous)
	d.Stats.Confirmed += len(dec.Confirmed)
	if res.Size == 1 {
		d.Stats.NewClusters++
	} else {
		d.Stats.Joined++
	}
	var best float64
	if len(ns) > 0 {
		best = ns[0].Similarity
	}
	d.log().Info("deduped", "item", id, "cluster", res.ClusterID, "primary", res.PrimaryID, "size", res.Size,
		"best_sim", best, "direct", dec.Direct, "ambiguous", dec.Ambiguous, "confirmed", dec.Confirmed)
	if err := d.Bus.PublishDeduped(ctx, natsbus.ItemEvent{ItemID: id, ClusterID: res.ClusterID, PrimaryID: res.PrimaryID}); err != nil {
		d.log().Warn("publish deduped failed", "item", id, "err", err)
	}
	return res, nil
}

func (d *Deduper) handleID(ctx context.Context, id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.DedupItem(ctx, id)
	switch {
	case err != nil:
		d.Stats.Failed++
		d.log().Error("dedup failed", "item", id, "err", err)
	case res.Skipped:
		d.Stats.Skipped++
	default:
		d.Stats.Processed++
	}
	return err
}

func (d *Deduper) handler(ctx context.Context) func(jetstream.Msg) {
	return func(msg jetstream.Msg) {
		var ev natsbus.ItemEvent
		if err := json.Unmarshal(msg.Data(), &ev); err != nil || ev.ItemID == 0 {
			_ = msg.Term()
			return
		}
		stop := heartbeat(msg)
		err := d.handleID(ctx, ev.ItemID)
		stop()
		settle(msg, err, d.log())
	}
}

// backfill clusters scored items that never got (or lost) their event.
func (d *Deduper) backfill(ctx context.Context) error {
	ids, err := d.Repo.PendingDedup(ctx, 100_000)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.handleID(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Drain consumes items.scored until empty, then clusters what is left.
func (d *Deduper) Drain(ctx context.Context, cons jetstream.Consumer) error {
	if cons != nil {
		if _, err := natsbus.Drain(ctx, cons, 1, d.handler(ctx)); err != nil {
			return err
		}
	}
	return d.backfill(ctx)
}

// Run consumes items.scored until ctx is cancelled and sweeps the database
// for scored, unclustered items at start and every BackfillEvery.
func (d *Deduper) Run(ctx context.Context, cons jetstream.Consumer) error {
	cc, err := cons.Consume(d.handler(ctx), jetstream.PullMaxMessages(1))
	if err != nil {
		return err
	}
	sweep := func() {
		if err := d.backfill(ctx); err != nil && ctx.Err() == nil {
			d.log().Error("dedup backfill", "err", err)
		}
	}
	sweep()
	every(ctx, d.BackfillEvery, sweep)
	cc.Stop()
	<-cc.Closed()
	return nil
}
