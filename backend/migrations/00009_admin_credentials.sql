-- +goose Up
-- +goose StatementBegin
-- Password reset ("esqueci minha senha"). Only the sha256 of a reset token
-- is stored; a used or expired token never works again. A reset saves the new
-- bcrypt hash here (it wins over ADMIN_PASSWORD_HASH) and bumps
-- session_version, which ends every session issued before it.
CREATE TABLE admin_credentials (
    username         TEXT        PRIMARY KEY,
    password_hash    TEXT        NOT NULL,
    session_version  INTEGER     NOT NULL DEFAULT 1,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE password_resets (
    id          BIGSERIAL   PRIMARY KEY,
    token_hash  TEXT        NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS password_resets;
DROP TABLE IF EXISTS admin_credentials;
-- +goose StatementEnd
