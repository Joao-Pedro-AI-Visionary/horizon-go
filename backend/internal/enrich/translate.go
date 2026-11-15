package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/domain"
)

// Translation backfill defaults.
const (
	TranslatorDurable = "items-translator"
	// DefaultTranslateAttempts is how many invalid model answers an item may
	// cost before the backfill gives up on it (it then counts as failed).
	DefaultTranslateAttempts = 3
	// DefaultTranslateBatch bounds one sweep so a backlog is spread over
	// several sweeps instead of one burst against the free model quota.
	DefaultTranslateBatch = 50
	// MaxTranslateBatch is the largest limit POST /api/pipeline/translate accepts.
	MaxTranslateBatch = 500
)

// TranslationRepo is the storage used by the translation backfill
// (postgres.AIRepo).
type TranslationRepo interface {
	PendingTranslations(ctx context.Context, maxAttempts, limit int) ([]postgres.TranslateRow, error)
	SaveTranslation(ctx context.Context, id int64, t domain.Translation) (bool, error)
	BumpTranslateAttempts(ctx context.Context, id int64) error
	TranslationStatus(ctx context.Context, maxAttempts int) (domain.TranslationStatus, error)
}

// ItemTranslator produces the pt-BR copy of one item (app.Analyzer). It must
// copy Portuguese text without a model call and wrap bad model output in
// app.ErrInvalidOutput.
type ItemTranslator interface {
	Translate(ctx context.Context, title, summary string) (domain.Translation, error)
}

// TranslateStats counts backfill outcomes since start.
type TranslateStats struct {
	Translated, Copied, Skipped, Failed atomic.Int64
}

// SweepResult is the outcome of one sweep.
type SweepResult struct {
	Translated int `json:"translated"`
	Copied     int `json:"copied"`
	Skipped    int `json:"skipped"`
	Failed     int `json:"failed"`
}

// ErrSweepBusy is returned by Sweep while another sweep is running.
var ErrSweepBusy = errors.New("a translation sweep is already running")

// Translator is the pt-BR translation backfill. It handles one item at a
// time; the model client paces the calls (LLM_RPM limiter shared with the
// scorer) and app.Analyzer retries each call (retry.Default).
type Translator struct {
	Repo  TranslationRepo
	Model ItemTranslator
	// MaxAttempts per item (default DefaultTranslateAttempts).
	MaxAttempts int
	// Batch is the default number of items per sweep (default DefaultTranslateBatch).
	Batch int
	// BackfillEvery is the period of the sweep in Run.
	BackfillEvery time.Duration
	Log           *slog.Logger

	running sync.Mutex
	Stats   TranslateStats
}

func (t *Translator) log() *slog.Logger {
	if t.Log == nil {
		return slog.Default()
	}
	return t.Log
}

func (t *Translator) maxAttempts() int {
	if t.MaxAttempts <= 0 {
		return DefaultTranslateAttempts
	}
	return t.MaxAttempts
}

func (t *Translator) batch(limit int) int {
	switch {
	case limit > 0:
		return min(limit, MaxTranslateBatch)
	case t.Batch > 0:
		return t.Batch
	default:
		return DefaultTranslateBatch
	}
}

// Status reports the backfill counters from the database.
func (t *Translator) Status(ctx context.Context) (domain.TranslationStatus, error) {
	return t.Repo.TranslationStatus(ctx, t.maxAttempts())
}

// Sweep translates up to limit pending items (0 = Batch), one at a time.
// Items that already have title_pt are never selected. An item whose model
// output stays invalid after the retries spends one attempt; any other
// error (outage, spent quota, cancellation) stops the sweep without
// spending attempts, so the next sweep picks the same items up again.
func (t *Translator) Sweep(ctx context.Context, limit int) (SweepResult, error) {
	var res SweepResult
	if !t.running.TryLock() {
		return res, ErrSweepBusy
	}
	defer t.running.Unlock()
	rows, err := t.Repo.PendingTranslations(ctx, t.maxAttempts(), t.batch(limit))
	if err != nil {
		return res, err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := t.translateRow(ctx, row, &res); err != nil && !errors.Is(err, app.ErrInvalidOutput) {
			return res, err
		}
	}
	return res, nil
}

func (t *Translator) translateRow(ctx context.Context, row postgres.TranslateRow, res *SweepResult) error {
	var tr domain.Translation
	var err error
	if strings.TrimSpace(row.Title) == "" {
		err = app.ErrInvalidOutput // nothing to translate: spend the attempts and leave the queue
	} else {
		tr, err = t.Model.Translate(ctx, row.Title, row.Summary)
	}
	if err != nil {
		if errors.Is(err, app.ErrInvalidOutput) {
			res.Failed++
			t.Stats.Failed.Add(1)
			t.log().Warn("translate failed", "item", row.ID, "attempt", row.Attempts+1, "err", err)
			if berr := t.Repo.BumpTranslateAttempts(context.WithoutCancel(ctx), row.ID); berr != nil {
				return errors.Join(err, berr)
			}
		}
		return err
	}
	saved, err := t.Repo.SaveTranslation(ctx, row.ID, tr)
	switch {
	case err != nil:
		return err
	case !saved:
		res.Skipped++
		t.Stats.Skipped.Add(1)
	case tr.Copied:
		res.Copied++
		t.Stats.Copied.Add(1)
	default:
		res.Translated++
		t.Stats.Translated.Add(1)
	}
	return nil
}

func (t *Translator) sweepAndLog(ctx context.Context, limit int) {
	res, err := t.Sweep(ctx, limit)
	switch {
	case errors.Is(err, ErrSweepBusy):
		t.log().Info("translate sweep skipped: already running")
		return
	case err != nil && ctx.Err() == nil:
		t.log().Error("translate sweep stopped", "err", err, "translated", res.Translated, "copied", res.Copied, "failed", res.Failed)
		return
	}
	if res != (SweepResult{}) {
		t.log().Info("translate sweep", "translated", res.Translated, "copied", res.Copied, "skipped", res.Skipped, "failed", res.Failed)
	}
}

// requestHandler acks each items.translate message at once (the sweep may
// outlast AckWait) and hands its limit to the loop; requests that arrive
// while one is queued are merged into it.
func requestHandler(trigger chan<- int) func(jetstream.Msg) {
	return func(msg jetstream.Msg) {
		var req natsbus.TranslateRequest
		_ = json.Unmarshal(msg.Data(), &req)
		_ = msg.Ack()
		select {
		case trigger <- req.Limit:
		default:
		}
	}
}

// Drain handles the queued requests (if any) with one sweep. Used by
// `worker --once`.
func (t *Translator) Drain(ctx context.Context, cons jetstream.Consumer) error {
	limit := 0
	if cons != nil {
		trigger := make(chan int, 1)
		if _, err := natsbus.Drain(ctx, cons, 1, requestHandler(trigger)); err != nil {
			return err
		}
		select {
		case limit = <-trigger:
		default:
		}
	}
	_, err := t.Sweep(ctx, limit)
	return err
}

// Run sweeps at start, every BackfillEvery and on each items.translate
// request (cons may be nil) until ctx is cancelled.
func (t *Translator) Run(ctx context.Context, cons jetstream.Consumer) error {
	trigger := make(chan int, 1)
	if cons != nil {
		cc, err := cons.Consume(requestHandler(trigger), jetstream.PullMaxMessages(1))
		if err != nil {
			return err
		}
		defer func() { cc.Stop(); <-cc.Closed() }()
	}
	every := t.BackfillEvery
	if every <= 0 {
		every = DefaultBackfillEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	t.sweepAndLog(ctx, 0)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			t.sweepAndLog(ctx, 0)
		case limit := <-trigger:
			t.sweepAndLog(ctx, limit)
		}
	}
}

// TranslationQueue is the API side of the backfill: it reads the counters
// and asks the worker (which owns the model) for a sweep over NATS.
type TranslationQueue struct {
	Repo        TranslationRepo
	Bus         *natsbus.Bus
	MaxAttempts int
}

// TranslationStatus reports the backfill counters.
func (q TranslationQueue) TranslationStatus(ctx context.Context) (domain.TranslationStatus, error) {
	attempts := q.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultTranslateAttempts
	}
	return q.Repo.TranslationStatus(ctx, attempts)
}

// RequestTranslation publishes one sweep request to the worker.
func (q TranslationQueue) RequestTranslation(ctx context.Context, limit int) error {
	return q.Bus.PublishTranslate(ctx, natsbus.TranslateRequest{Limit: limit})
}
