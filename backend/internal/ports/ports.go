package ports

import (
	"context"
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
