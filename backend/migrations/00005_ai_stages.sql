-- +goose Up
-- +goose StatementBegin
-- Phase 2: scoring/dedup bookkeeping and the model output cache.
ALTER TABLE items
    ADD COLUMN score_attempts INTEGER     NOT NULL DEFAULT 0,
    ADD COLUMN last_error     TEXT        NOT NULL DEFAULT '',
    ADD COLUMN scored_at      TIMESTAMPTZ,
    ADD COLUMN deduped_at     TIMESTAMPTZ;

-- Work queues of the scorer (status 'raw') and the deduper (status 'scored').
CREATE INDEX items_pending_ai_idx ON items (status, id) WHERE status IN ('raw', 'scored');
-- Time window of the neighbour search.
CREATE INDEX items_event_time_idx ON items ((COALESCE(published_at, fetched_at)));

-- Model outputs keyed by kind ('score', 'embed', 'dedup') and the hash of the
-- prompt version, model and content: the same text is never sent twice.
CREATE TABLE ai_cache (
    kind        TEXT        NOT NULL,
    key         TEXT        NOT NULL,
    model       TEXT        NOT NULL,
    value       JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, key)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_cache;
DROP INDEX IF EXISTS items_event_time_idx;
DROP INDEX IF EXISTS items_pending_ai_idx;
ALTER TABLE items
    DROP COLUMN IF EXISTS deduped_at,
    DROP COLUMN IF EXISTS scored_at,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS score_attempts;
-- +goose StatementEnd
