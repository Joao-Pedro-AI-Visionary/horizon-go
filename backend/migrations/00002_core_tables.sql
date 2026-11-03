-- +goose Up
-- +goose StatementBegin
CREATE TABLE sources (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT        NOT NULL CHECK (kind IN ('hackernews', 'reddit', 'rss')),
    name        TEXT        NOT NULL,
    config      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (kind, name)
);

CREATE TABLE clusters (
    id                     BIGSERIAL PRIMARY KEY,
    representative_item_id BIGINT,
    title                  TEXT        NOT NULL DEFAULT '',
    centroid               vector(768),
    item_count             INTEGER     NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE items (
    id              BIGSERIAL PRIMARY KEY,
    source_id       BIGINT      NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    external_id     TEXT        NOT NULL,
    url             TEXT        NOT NULL,
    url_normalized  TEXT        NOT NULL,
    title           TEXT        NOT NULL,
    content         TEXT        NOT NULL DEFAULT '',
    author          TEXT        NOT NULL DEFAULT '',
    published_at    TIMESTAMPTZ,
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    status          TEXT        NOT NULL DEFAULT 'raw'
                    CHECK (status IN ('raw', 'scored', 'deduped', 'duplicate', 'failed')),
    score           REAL        CHECK (score IS NULL OR (score >= 0 AND score <= 10)),
    score_reason    TEXT,
    summary         TEXT,
    tags            TEXT[]      NOT NULL DEFAULT '{}',
    embedding       vector(768),
    cluster_id      BIGINT      REFERENCES clusters (id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_id, external_id)
);

ALTER TABLE clusters
    ADD CONSTRAINT clusters_representative_item_fk
    FOREIGN KEY (representative_item_id) REFERENCES items (id) ON DELETE SET NULL;

CREATE INDEX items_url_normalized_idx   ON items (url_normalized);
CREATE INDEX items_published_at_idx     ON items (published_at DESC);
CREATE INDEX items_status_idx           ON items (status);
CREATE INDEX items_cluster_id_idx       ON items (cluster_id);
CREATE INDEX items_embedding_hnsw_idx   ON items USING hnsw (embedding vector_cosine_ops);
CREATE INDEX clusters_centroid_hnsw_idx ON clusters USING hnsw (centroid vector_cosine_ops);

CREATE TABLE digests (
    id           BIGSERIAL PRIMARY KEY,
    digest_date  DATE        NOT NULL,
    language     TEXT        NOT NULL DEFAULT 'pt-BR',
    title        TEXT        NOT NULL,
    markdown     TEXT        NOT NULL,
    html         TEXT        NOT NULL DEFAULT '',
    item_ids     BIGINT[]    NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (digest_date, language)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS digests;
ALTER TABLE IF EXISTS clusters DROP CONSTRAINT IF EXISTS clusters_representative_item_fk;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS clusters;
DROP TABLE IF EXISTS sources;
-- +goose StatementEnd
