package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
)

// TestCredentialRepoAgainstPostgres: tokens are single use, expire, and a
// reset bumps the session version.
func TestCredentialRepoAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := testDB(t, ctx)
	if _, err := pool.Exec(ctx, `TRUNCATE admin_credentials, password_resets`); err != nil {
		t.Fatal(err)
	}
	repo := postgres.CredentialRepo{DB: pool}
	now := time.Now()
	if _, found, err := repo.GetCredential(ctx, "joao"); err != nil || found {
		t.Fatalf("empty: %v %v", found, err)
	}
	a, b := HashResetToken("token-a"), HashResetToken("token-b")
	if err := repo.CreateReset(ctx, a, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateReset(ctx, b, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repo.ConsumeReset(ctx, b, "joao", "h0", now); err != nil || ok {
		t.Fatalf("expired token accepted: %v %v", ok, err)
	}
	c, ok, err := repo.ConsumeReset(ctx, a, "joao", "h1", now)
	if err != nil || !ok || c.PasswordHash != "h1" || c.SessionVersion != 1 {
		t.Fatalf("first reset: %+v %v %v", c, ok, err)
	}
	if _, ok, _ := repo.ConsumeReset(ctx, a, "joao", "h2", now); ok {
		t.Fatal("token reused")
	}
	cc := HashResetToken("token-c")
	_ = repo.CreateReset(ctx, cc, now.Add(30*time.Minute))
	c, ok, err = repo.ConsumeReset(ctx, cc, "joao", "h3", now)
	if err != nil || !ok || c.SessionVersion != 2 {
		t.Fatalf("second reset: %+v %v %v", c, ok, err)
	}
	got, found, err := repo.GetCredential(ctx, "joao")
	if err != nil || !found || got.PasswordHash != "h3" || got.SessionVersion != 2 {
		t.Fatalf("stored: %+v %v %v", got, found, err)
	}
	var stored int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM password_resets WHERE token_hash IN ('token-a','token-b','token-c')`).Scan(&stored)
	if stored != 0 {
		t.Fatal("raw tokens stored")
	}
}
