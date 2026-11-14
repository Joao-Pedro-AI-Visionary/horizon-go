-- +goose Up
-- +goose StatementBegin
-- Persistent watermark for the internal scheduler. Render free services sleep
-- after 15 min idle and restart on wake, so boot catch-up reads this table to
-- run missed slots idempotently and avoid pre-install cleanup deletes.
CREATE TABLE scheduler_state (
    job_name        TEXT        PRIMARY KEY,
    installed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_slot_at    TIMESTAMPTZ,
    last_run_at     TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_status     TEXT        NOT NULL DEFAULT '',
    last_error      TEXT        NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO scheduler_state (job_name) VALUES ('collect'), ('cleanup');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS scheduler_state;
-- +goose StatementEnd
