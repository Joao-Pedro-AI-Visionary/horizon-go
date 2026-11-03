-- +goose Up
-- +goose StatementBegin
-- Phase 1: items are upserted by the hash of the normalized URL, so one row
-- per URL across sources. The (source_id, external_id) unique key would make a
-- re-fetched post whose URL changed fail the upsert, so it becomes an index.
ALTER TABLE items ADD COLUMN url_hash TEXT;
UPDATE items SET url_hash = encode(sha256(convert_to(url_normalized, 'UTF8')), 'hex');
ALTER TABLE items ALTER COLUMN url_hash SET NOT NULL;
ALTER TABLE items ADD CONSTRAINT items_url_hash_key UNIQUE (url_hash);
ALTER TABLE items DROP CONSTRAINT items_source_id_external_id_key;
CREATE INDEX items_source_external_idx ON items (source_id, external_id);

-- Per-source report of each collector run.
CREATE TABLE fetch_runs (
    id           BIGSERIAL PRIMARY KEY,
    run_id       TEXT        NOT NULL,
    source_id    BIGINT      NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    status       TEXT        NOT NULL CHECK (status IN ('ok', 'error')),
    item_count   INTEGER     NOT NULL DEFAULT 0,
    error        TEXT        NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX fetch_runs_run_id_idx         ON fetch_runs (run_id);
CREATE INDEX fetch_runs_source_started_idx ON fetch_runs (source_id, started_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS fetch_runs;
DROP INDEX IF EXISTS items_source_external_idx;
ALTER TABLE items ADD CONSTRAINT items_source_id_external_id_key UNIQUE (source_id, external_id);
ALTER TABLE items DROP CONSTRAINT IF EXISTS items_url_hash_key;
ALTER TABLE items DROP COLUMN IF EXISTS url_hash;
-- +goose StatementEnd
