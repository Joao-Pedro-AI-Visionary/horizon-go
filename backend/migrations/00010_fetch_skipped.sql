-- +goose Up
-- +goose StatementBegin
-- A source may skip a run (e.g. Reddit rate limiting): recorded as 'skipped',
-- which is neither ok nor an error.
--
-- Was 00007_fetch_skipped.sql, which collided with 00007_push_sources.sql
-- (goose refuses duplicate versions). Production applied 00007_push_sources
-- and never this one. A database that applied the old 00007_fetch_skipped
-- instead (local dev on the fix-reddit-rss branch) has version 7 recorded
-- without the 'push' source kind, so the kind check is re-asserted here too.
-- Both statements are idempotent.
ALTER TABLE fetch_runs DROP CONSTRAINT IF EXISTS fetch_runs_status_check;
ALTER TABLE fetch_runs ADD CONSTRAINT fetch_runs_status_check CHECK (status IN ('ok', 'error', 'skipped'));
ALTER TABLE sources DROP CONSTRAINT IF EXISTS sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check
    CHECK (kind IN ('hackernews', 'reddit', 'rss', 'push'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE fetch_runs SET status = 'error' WHERE status = 'skipped';
ALTER TABLE fetch_runs DROP CONSTRAINT IF EXISTS fetch_runs_status_check;
ALTER TABLE fetch_runs ADD CONSTRAINT fetch_runs_status_check CHECK (status IN ('ok', 'error'));
-- +goose StatementEnd
