-- +goose Up
-- +goose StatementBegin
-- Phase 3: runtime settings (one row), model token usage per day and the
-- digest bookkeeping.
CREATE TABLE settings (
    id                 SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    score_threshold    REAL        NOT NULL DEFAULT 6 CHECK (score_threshold >= 0 AND score_threshold <= 10),
    digest_max_items   INTEGER     NOT NULL DEFAULT 20 CHECK (digest_max_items BETWEEN 1 AND 200),
    digest_per_source  INTEGER     NOT NULL DEFAULT 8 CHECK (digest_per_source BETWEEN 0 AND 200),
    digest_cron        TEXT        NOT NULL DEFAULT '0 7 * * *',
    timezone           TEXT        NOT NULL DEFAULT 'America/Sao_Paulo',
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO settings (id) VALUES (1);

-- Ollama prompt_eval_count / eval_count, aggregated per UTC day, kind and model.
CREATE TABLE llm_usage (
    day                DATE        NOT NULL,
    kind               TEXT        NOT NULL CHECK (kind IN ('chat', 'embed')),
    model              TEXT        NOT NULL,
    calls              BIGINT      NOT NULL DEFAULT 0,
    prompt_tokens      BIGINT      NOT NULL DEFAULT 0,
    completion_tokens  BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (day, kind, model)
);

ALTER TABLE digests
    ADD COLUMN considered   INTEGER     NOT NULL DEFAULT 0,
    ADD COLUMN window_start TIMESTAMPTZ,
    ADD COLUMN window_end   TIMESTAMPTZ,
    ADD COLUMN updated_at   TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX feed_view_event_time_idx ON feed_view ((COALESCE(published_at, projected_at)) DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS feed_view_event_time_idx;
ALTER TABLE digests
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS window_end,
    DROP COLUMN IF EXISTS window_start,
    DROP COLUMN IF EXISTS considered;
DROP TABLE IF EXISTS llm_usage;
DROP TABLE IF EXISTS settings;
-- +goose StatementEnd
