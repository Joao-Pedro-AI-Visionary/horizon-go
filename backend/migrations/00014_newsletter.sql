-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS newsletter_subscribers (
    id                 BIGSERIAL PRIMARY KEY,
    email              TEXT NOT NULL UNIQUE,
    status             TEXT NOT NULL CHECK (status IN ('pending', 'active', 'unsubscribed')),
    confirm_token_hash TEXT,
    confirm_expires_at TIMESTAMPTZ,
    unsub_token        TEXT NOT NULL UNIQUE,
    confirmed_at       TIMESTAMPTZ,
    unsubscribed_at    TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS newsletter_confirm_hash_idx
    ON newsletter_subscribers (confirm_token_hash)
    WHERE confirm_token_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS newsletter_deliveries (
    edition_date DATE PRIMARY KEY,
    item_ids     BIGINT[] NOT NULL,
    sent_count   INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL CHECK (status IN ('ok', 'partial', 'error', 'skipped')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS newsletter_deliveries;
DROP TABLE IF EXISTS newsletter_subscribers;
-- +goose StatementEnd
