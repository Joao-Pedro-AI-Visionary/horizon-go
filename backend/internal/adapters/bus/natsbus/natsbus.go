// Package natsbus is the NATS JetStream event bus: stream ITEMS with subjects
// <prefix>.> ; raw items go to <prefix>.raw.
package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ports"
)

// Defaults for production use.
const (
	DefaultStream        = "ITEMS"
	DefaultSubjectPrefix = "items"
	DefaultDurable       = "items-persister"
	maxDeliver           = 5
	nakDelay             = 2 * time.Second
	fetchBatch           = 100
	fetchWait            = time.Second
)

// Config selects the server, stream and subject namespace.
type Config struct {
	URL           string
	Stream        string
	SubjectPrefix string
}

// Bus publishes and consumes items on JetStream.
type Bus struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	stream string
	prefix string
}

// Connect dials NATS and creates or updates the stream.
func Connect(ctx context.Context, cfg Config) (*Bus, error) {
	if cfg.Stream == "" {
		cfg.Stream = DefaultStream
	}
	if cfg.SubjectPrefix == "" {
		cfg.SubjectPrefix = DefaultSubjectPrefix
	}
	nc, err := nats.Connect(cfg.URL, nats.Name("horizon-go"))
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       cfg.Stream,
		Subjects:   []string{cfg.SubjectPrefix + ".>"},
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		MaxAge:     7 * 24 * time.Hour,
		Duplicates: 2 * time.Minute,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats stream %s: %w", cfg.Stream, err)
	}
	return &Bus{nc: nc, js: js, stream: cfg.Stream, prefix: cfg.SubjectPrefix}, nil
}

// Close drains the connection.
func (b *Bus) Close() { _ = b.nc.Drain() }

// RawSubject is the subject carrying raw items.
func (b *Bus) RawSubject() string { return b.prefix + ".raw" }

// JetStream exposes the context (used by tests for cleanup).
func (b *Bus) JetStream() jetstream.JetStream { return b.js }

// PublishRaw publishes each item as JSON, awaiting the JetStream ack. The
// message id (run id + url hash) lets the server drop retried duplicates.
func (b *Bus) PublishRaw(ctx context.Context, runID string, items []domain.Item) error {
	for _, it := range items {
		data, err := json.Marshal(it)
		if err != nil {
			return err
		}
		if _, err := b.js.Publish(ctx, b.RawSubject(), data, jetstream.WithMsgID(runID+":"+it.URLHash)); err != nil {
			return fmt.Errorf("publish %s: %w", it.URLHash, err)
		}
	}
	return nil
}

// Persister is the durable consumer that upserts raw items into Postgres.
type Persister struct {
	cons jetstream.Consumer
	repo ports.ItemRepo
	log  *slog.Logger
}

// NewPersister creates or updates the durable pull consumer on <prefix>.raw.
func (b *Bus) NewPersister(ctx context.Context, durable string, repo ports.ItemRepo, log *slog.Logger) (*Persister, error) {
	if durable == "" {
		durable = DefaultDurable
	}
	if log == nil {
		log = slog.Default()
	}
	cons, err := b.js.CreateOrUpdateConsumer(ctx, b.stream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: b.RawSubject(),
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    maxDeliver,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("nats consumer %s: %w", durable, err)
	}
	return &Persister{cons: cons, repo: repo, log: log}, nil
}

// handle returns true when the item was persisted.
func (p *Persister) handle(ctx context.Context, msg jetstream.Msg) bool {
	var it domain.Item
	if err := json.Unmarshal(msg.Data(), &it); err != nil {
		p.log.Error("drop malformed item message", "err", err)
		_ = msg.Term()
		return false
	}
	if err := it.Prepare(); err != nil {
		p.log.Error("drop item with invalid url", "url", it.URL, "err", err)
		_ = msg.Term()
		return false
	}
	if _, err := p.repo.Upsert(ctx, it); err != nil {
		p.log.Error("upsert failed, will retry", "url", it.URL, "err", err)
		_ = msg.NakWithDelay(nakDelay)
		return false
	}
	if err := msg.Ack(); err != nil {
		p.log.Warn("ack failed", "err", err)
	}
	return true
}

// Drain consumes until the consumer has nothing pending, then returns the
// number of persisted items. Used by `worker --once`.
func (p *Persister) Drain(ctx context.Context) (int, error) {
	persisted := 0
	for {
		batch, err := p.cons.Fetch(fetchBatch, jetstream.FetchMaxWait(fetchWait))
		if err != nil {
			return persisted, err
		}
		n := 0
		for msg := range batch.Messages() {
			n++
			if p.handle(ctx, msg) {
				persisted++
			}
		}
		if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) {
			return persisted, err
		}
		if n > 0 {
			continue
		}
		info, err := p.cons.Info(ctx)
		if err != nil {
			return persisted, err
		}
		if info.NumPending == 0 && info.NumAckPending == 0 {
			return persisted, nil
		}
		if err := ctx.Err(); err != nil {
			return persisted, err
		}
	}
}

// Run consumes continuously until ctx is cancelled.
func (p *Persister) Run(ctx context.Context) error {
	cc, err := p.cons.Consume(func(msg jetstream.Msg) { p.handle(ctx, msg) })
	if err != nil {
		return err
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}

// ScoredSubject carries items that got a score and an embedding.
func (b *Bus) ScoredSubject() string { return b.prefix + ".scored" }

// DedupedSubject carries items that were placed in a cluster.
func (b *Bus) DedupedSubject() string { return b.prefix + ".deduped" }

// ItemEvent is the payload of items.scored and items.deduped.
type ItemEvent struct {
	ItemID    int64 `json:"item_id"`
	ClusterID int64 `json:"cluster_id,omitempty"`
	PrimaryID int64 `json:"primary_id,omitempty"`
}

func (b *Bus) publishEvent(ctx context.Context, subject, msgID string, ev ItemEvent) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := b.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// PublishScored announces that an item was scored.
func (b *Bus) PublishScored(ctx context.Context, itemID int64) error {
	return b.publishEvent(ctx, b.ScoredSubject(), fmt.Sprintf("scored:%d", itemID), ItemEvent{ItemID: itemID})
}

// PublishDeduped announces that an item joined a cluster.
func (b *Bus) PublishDeduped(ctx context.Context, ev ItemEvent) error {
	return b.publishEvent(ctx, b.DedupedSubject(), fmt.Sprintf("deduped:%d:%d", ev.ItemID, ev.ClusterID), ev)
}

// Consumer creates or updates a durable pull consumer on subject. ackWait
// must cover the slowest handler (a cold model load takes about a minute).
func (b *Bus) Consumer(ctx context.Context, durable, subject string, ackWait time.Duration) (jetstream.Consumer, error) {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, b.stream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    maxDeliver,
		AckWait:       ackWait,
	})
	if err != nil {
		return nil, fmt.Errorf("nats consumer %s: %w", durable, err)
	}
	return cons, nil
}

// MaxDeliver is the redelivery limit of every consumer.
const MaxDeliver = maxDeliver

// Drain fetches up to batchSize messages at a time from cons and hands them
// to handle until nothing is pending or in flight. handle must ack, nak or
// term each message; a small batch keeps slow handlers within the ack wait.
func Drain(ctx context.Context, cons jetstream.Consumer, batchSize int, handle func(jetstream.Msg)) (int, error) {
	if batchSize <= 0 {
		batchSize = fetchBatch
	}
	seen := 0
	for {
		batch, err := cons.Fetch(batchSize, jetstream.FetchMaxWait(fetchWait))
		if err != nil {
			return seen, err
		}
		n := 0
		for msg := range batch.Messages() {
			n++
			handle(msg)
		}
		seen += n
		if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) {
			return seen, err
		}
		if n > 0 {
			continue
		}
		info, err := cons.Info(ctx)
		if err != nil {
			return seen, err
		}
		if info.NumPending == 0 && info.NumAckPending == 0 {
			return seen, nil
		}
		if err := ctx.Err(); err != nil {
			return seen, err
		}
	}
}
