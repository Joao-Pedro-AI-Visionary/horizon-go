-- +goose Up
-- +goose StatementBegin
-- Failures that leave no other trace (today: digest generation). The admin
-- alerts endpoint (GET /api/admin/alerts) reads them next to fetch_runs
-- errors and items that could not be scored.
CREATE TABLE alert_events (
    id       BIGSERIAL   PRIMARY KEY,
    kind     TEXT        NOT NULL,
    name     TEXT        NOT NULL,
    message  TEXT        NOT NULL,
    at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX alert_events_at_idx ON alert_events (at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS alert_events;
-- +goose StatementEnd
