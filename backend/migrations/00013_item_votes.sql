-- +goose Up
-- +goose StatementBegin
-- Anonymous per-device item votes (one device, one vote per item) and
-- short-lived salted IP rate-limit records (no raw IPs stored, <= 24h TTL).
CREATE TABLE item_votes (
    item_id     BIGINT      NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    device_hash TEXT        NOT NULL,
    vote        SMALLINT    NOT NULL CHECK (vote IN (-1, 1)),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (item_id, device_hash)
);

CREATE INDEX item_votes_item_idx ON item_votes (item_id);

CREATE TABLE vote_rate_limits (
    id          BIGSERIAL   PRIMARY KEY,
    ip_hash     TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX vote_rate_limits_lookup_idx ON vote_rate_limits (ip_hash, created_at DESC);
CREATE INDEX vote_rate_limits_expires_idx ON vote_rate_limits (expires_at);

ALTER TABLE feed_view
    ADD COLUMN likes_count    INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN dislikes_count INTEGER NOT NULL DEFAULT 0;

CREATE INDEX feed_view_trending_idx ON feed_view ((COALESCE(published_at, projected_at)) DESC, score DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS feed_view_trending_idx;
ALTER TABLE feed_view
    DROP COLUMN IF EXISTS dislikes_count,
    DROP COLUMN IF EXISTS likes_count;
DROP TABLE IF EXISTS vote_rate_limits;
DROP TABLE IF EXISTS item_votes;
-- +goose StatementEnd
