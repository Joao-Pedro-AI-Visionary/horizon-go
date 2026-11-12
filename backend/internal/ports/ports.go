package ports

import (
	"context"
	"errors"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

// Source fetches items published after since.
type Source interface {
	Name() string
	Fetch(ctx context.Context, since time.Time) ([]domain.Item, error)
}

// SourceRepo is the CRUD store for configured sources.
type SourceRepo interface {
	Create(ctx context.Context, s domain.SourceConfig) (domain.SourceConfig, error)
	Get(ctx context.Context, id int64) (domain.SourceConfig, error)
	List(ctx context.Context, enabledOnly bool) ([]domain.SourceConfig, error)
	Update(ctx context.Context, s domain.SourceConfig) (domain.SourceConfig, error)
	Delete(ctx context.Context, id int64) error
}

// ItemRepo persists items. Upsert is idempotent by URL hash.
type ItemRepo interface {
	Upsert(ctx context.Context, it domain.Item) (int64, error)
}

// FetchRunRepo stores per-source fetch reports.
type FetchRunRepo interface {
	Insert(ctx context.Context, r domain.FetchRun) (int64, error)
}

// ItemPublisher puts raw items on the event bus.
type ItemPublisher interface {
	PublishRaw(ctx context.Context, runID string, items []domain.Item) error
}

// ChatRequest is one completion call. JSON asks the model for a JSON object.
type ChatRequest struct {
	System string
	User   string
	JSON   bool
}

// LLM is a chat-completion model.
type LLM interface {
	Model() string
	Chat(ctx context.Context, req ChatRequest) (string, error)
}

// ErrEmbeddingUnavailable is returned (wrapped) by an Embedder whose quota is
// spent (e.g. the Workers AI daily free allocation). Callers degrade to "no
// embedding" instead of failing: the item is scored but not deduplicated.
var ErrEmbeddingUnavailable = errors.New("embedding unavailable")

// Embedder turns texts into vectors (one per input, same order).
type Embedder interface {
	Model() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// AICache stores model outputs keyed by kind + content hash.
type AICache interface {
	Get(ctx context.Context, kind, key string) ([]byte, bool, error)
	Put(ctx context.Context, kind, key, model string, value []byte) error
}
