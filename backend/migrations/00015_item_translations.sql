-- +goose Up
-- +goose StatementBegin
-- pt-BR copies of the title and the AI summary. The originals (title,
-- summary) are never overwritten. pt_source tells whether the text came from
-- the translation model ('llm') or was already Portuguese ('copy').
ALTER TABLE items
    ADD COLUMN IF NOT EXISTS title_pt           TEXT,
    ADD COLUMN IF NOT EXISTS summary_pt         TEXT,
    ADD COLUMN IF NOT EXISTS pt_source          TEXT CHECK (pt_source IN ('llm', 'copy')),
    ADD COLUMN IF NOT EXISTS translate_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS translated_at      TIMESTAMPTZ;

ALTER TABLE feed_view
    ADD COLUMN IF NOT EXISTS title_pt   TEXT,
    ADD COLUMN IF NOT EXISTS summary_pt TEXT;

-- The translation backfill sweeps the processed items still without a copy.
CREATE INDEX IF NOT EXISTS items_pending_translation_idx
    ON items (id)
    WHERE title_pt IS NULL AND status IN ('scored', 'deduped', 'duplicate');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS items_pending_translation_idx;
ALTER TABLE feed_view
    DROP COLUMN IF EXISTS summary_pt,
    DROP COLUMN IF EXISTS title_pt;
ALTER TABLE items
    DROP COLUMN IF EXISTS translated_at,
    DROP COLUMN IF EXISTS translate_attempts,
    DROP COLUMN IF EXISTS pt_source,
    DROP COLUMN IF EXISTS summary_pt,
    DROP COLUMN IF EXISTS title_pt;
-- +goose StatementEnd
