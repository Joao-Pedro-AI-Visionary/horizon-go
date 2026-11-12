-- +goose Up
-- +goose StatementBegin
-- 'push' sources are never fetched by the collector: their items arrive
-- through POST /api/items/ingest (e.g. the X radar routine).
ALTER TABLE sources DROP CONSTRAINT IF EXISTS sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check
    CHECK (kind IN ('hackernews', 'reddit', 'rss', 'push'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM sources WHERE kind = 'push';
ALTER TABLE sources DROP CONSTRAINT IF EXISTS sources_kind_check;
ALTER TABLE sources ADD CONSTRAINT sources_kind_check
    CHECK (kind IN ('hackernews', 'reddit', 'rss'));
-- +goose StatementEnd
