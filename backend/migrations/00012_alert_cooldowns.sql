-- +goose Up
-- +goose StatementBegin
-- Persistent per-alert-type e-mail cooldown so concurrent or restarted
-- instances never send more than one e-mail per kind every 6 hours.
CREATE TABLE alert_cooldowns (
    kind         TEXT        PRIMARY KEY,
    last_sent_at TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS alert_cooldowns;
-- +goose StatementEnd
