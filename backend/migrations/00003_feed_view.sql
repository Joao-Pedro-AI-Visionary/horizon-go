-- +goose Up
-- +goose StatementBegin
-- feed_view is the CQRS read model (projection), maintained by a consumer.
-- The read API only queries this table.
CREATE TABLE feed_view (
    item_id          BIGINT      PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    source_id        BIGINT      NOT NULL,
    source_kind      TEXT        NOT NULL,
    source_name      TEXT        NOT NULL,
    title            TEXT        NOT NULL,
    url              TEXT        NOT NULL,
    summary          TEXT        NOT NULL DEFAULT '',
    score_reason     TEXT        NOT NULL DEFAULT '',
    score            REAL        NOT NULL,
    tags             TEXT[]      NOT NULL DEFAULT '{}',
    published_at     TIMESTAMPTZ,
    cluster_id       BIGINT,
    duplicate_count  INTEGER     NOT NULL DEFAULT 0,
    projected_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX feed_view_score_idx        ON feed_view (score DESC, published_at DESC);
CREATE INDEX feed_view_published_at_idx ON feed_view (published_at DESC);
CREATE INDEX feed_view_source_kind_idx  ON feed_view (source_kind);
CREATE INDEX feed_view_tags_gin_idx     ON feed_view USING gin (tags);
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS feed_view;
