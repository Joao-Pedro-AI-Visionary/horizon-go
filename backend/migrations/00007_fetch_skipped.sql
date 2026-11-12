-- +goose Up
-- +goose StatementBegin
-- A source may skip a run (e.g. Reddit rate limiting): recorded as 'skipped',
-- which is neither ok nor an error.
ALTER TABLE fetch_runs DROP CONSTRAINT IF EXISTS fetch_runs_status_check;
ALTER TABLE fetch_runs ADD CONSTRAINT fetch_runs_status_check CHECK (status IN ('ok', 'error', 'skipped'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE fetch_runs SET status = 'error' WHERE status = 'skipped';
ALTER TABLE fetch_runs DROP CONSTRAINT IF EXISTS fetch_runs_status_check;
ALTER TABLE fetch_runs ADD CONSTRAINT fetch_runs_status_check CHECK (status IN ('ok', 'error'));
-- +goose StatementEnd
