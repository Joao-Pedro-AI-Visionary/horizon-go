package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jpdev/horizon-go/internal/domain"
)

// CredentialRepo stores the admin password set by a reset and the reset
// tokens (sha256 only).
type CredentialRepo struct{ DB *pgxpool.Pool }

// GetCredential returns the stored hash for username, if a reset saved one.
func (r CredentialRepo) GetCredential(ctx context.Context, username string) (domain.AdminCredential, bool, error) {
	var c domain.AdminCredential
	err := r.DB.QueryRow(ctx, `SELECT password_hash, session_version FROM admin_credentials WHERE username = $1`, username).
		Scan(&c.PasswordHash, &c.SessionVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AdminCredential{}, false, nil
	}
	return c, err == nil, err
}

// CreateReset stores a new token hash (and drops the ones long gone).
func (r CredentialRepo) CreateReset(ctx context.Context, tokenHash string, expiresAt time.Time) error {
	if _, err := r.DB.Exec(ctx, `DELETE FROM password_resets WHERE expires_at < now() - interval '7 days'`); err != nil {
		return err
	}
	_, err := r.DB.Exec(ctx, `INSERT INTO password_resets (token_hash, expires_at) VALUES ($1, $2)`, tokenHash, expiresAt)
	return err
}

// ConsumeReset uses the token once, saves newHash and bumps the session
// version; every other open token is closed too.
func (r CredentialRepo) ConsumeReset(ctx context.Context, tokenHash, username, newHash string, now time.Time) (domain.AdminCredential, bool, error) {
	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return domain.AdminCredential{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	err = tx.QueryRow(ctx, `
UPDATE password_resets SET used_at = $2
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
RETURNING id`, tokenHash, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AdminCredential{}, false, nil
	}
	if err != nil {
		return domain.AdminCredential{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = $1 WHERE used_at IS NULL`, now); err != nil {
		return domain.AdminCredential{}, false, err
	}
	var c domain.AdminCredential
	if err := tx.QueryRow(ctx, `
INSERT INTO admin_credentials (username, password_hash) VALUES ($1, $2)
ON CONFLICT (username) DO UPDATE
   SET password_hash = EXCLUDED.password_hash,
       session_version = admin_credentials.session_version + 1,
       updated_at = now()
RETURNING password_hash, session_version`, username, newHash).Scan(&c.PasswordHash, &c.SessionVersion); err != nil {
		return domain.AdminCredential{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AdminCredential{}, false, err
	}
	return c, true, nil
}
