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
	// AckWait covers a cold model load plus retries.
	AckWait  = 5 * time.Minute
	nakDelay = 5 * time.Second
)

func settle(msg jetstream.Msg, err error, log *slog.Logger) {
	if msg == nil {
		return
	}
	if err == nil {
		_ = msg.Ack()
		return
	}
	if md, mdErr := msg.Metadata(); mdErr == nil && md.NumDelivered >= natsbus.MaxDeliver {
		log.Warn("giving up on message, backfill will retry from the database", "subject", msg.Subject(), "err", err)
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
	Log         *slog.Logger

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
// returns false when the item already had a score.
func (s *Scorer) ScoreItem(ctx context.Context, id int64) (bool, error) {
	row, err := s.Repo.LoadScoreRow(ctx, id)
	if err != nil {
		return false, err
	}
	if row.Scored {
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
	if j.msg != nil {
		_ = j.msg.InProgress()
	}
	ok, err := s.ScoreItem(ctx, j.id)
	switch {
	case err != nil:
		s.Stats.Failed.Add(1)
		s.log().Error("score failed", "item", j.id, "err", err)
		if rerr := s.Repo.RecordScoreFailure(ctx, j.id, err, s.maxAttempts()); rerr != nil {
			s.log().Error("record failure", "item", j.id, "err", rerr)
		}
	case ok:
		s.Stats.Scored.Add(1)
	default:
		s.Stats.Skipped.Add(1)
	}
	settle(j.msg, err, s.log())
}

// startPool runs the workers; close jobs and call the returned wait.
func (s *Scorer) startPool(ctx context.Context) (chan<- scoreJob, func()) {
	jobs := make(chan scoreJob)
	var wg sync.WaitGroup
	for range s.workers() {
		wg.Go(func() {
			for j := range jobs {
				s.process(ctx, j)
			}
		})
	}
	return jobs, func() { close(jobs); wg.Wait() }
}

// rawHandler resolves an items.raw message to its row and queues it.
func (s *Scorer) rawHandler(ctx context.Context, jobs chan<- scoreJob) func(jetstream.Msg) {
	return func(msg jetstream.Msg) {
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

// Run backfills, then consumes items.raw until ctx is cancelled.
func (s *Scorer) Run(ctx context.Context, cons jetstream.Consumer) error {
	jobs, wait := s.startPool(ctx)
	// Callbacks may still be running after Stop; the pool closes only after
	// every one of them returned (they give up on ctx.Done).
	var inflight sync.WaitGroup
	defer func() { inflight.Wait(); wait() }()
	if err := s.backfill(ctx, jobs); err != nil {
		return err
	}
	h := s.rawHandler(ctx, jobs)
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		inflight.Add(1)
		defer inflight.Done()
		h(msg)
	}, jetstream.PullMaxMessages(s.workers()))
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
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

// Deduper is the dedup stage. It runs serially: cluster writes also take an
// advisory lock, so several processes stay consistent.
type Deduper struct {
	Repo       postgres.AIRepo
	Analyzer   *app.Analyzer
	Bus        *natsbus.Bus
	Thresholds domain.Thresholds
	Window     time.Duration
	Neighbors  int
	Log        *slog.Logger

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
		_ = msg.InProgress()
		settle(msg, d.handleID(ctx, ev.ItemID), d.log())
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

// Run backfills, then consumes items.scored until ctx is cancelled.
func (d *Deduper) Run(ctx context.Context, cons jetstream.Consumer) error {
	if err := d.backfill(ctx); err != nil {
		d.log().Error("dedup backfill", "err", err)
	}
	cc, err := cons.Consume(d.handler(ctx), jetstream.PullMaxMessages(1))
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}
