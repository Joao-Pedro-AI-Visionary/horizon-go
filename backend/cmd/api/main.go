// Command api serves the REST API of the dashboard (Phase 3).
//
// Environment (defaults from CLAUDE.md):
//
//	DATABASE_URL    full DSN; or PGUSER/PGPASSWORD/PGHOST/PGPORT/PGDATABASE
//	                (defaults: local dev container, see .env.example)
//	NATS_URL        nats://localhost:4230
//	API_ADDR        :8080
//	HORIZON_CORS_ORIGINS  origins allowed to call the API from the browser
//	                (comma separated; CORS_ORIGINS is still accepted). Default:
//	                the horizon-web dev and preview servers, see defaultCORSOrigins.
//	PORT            used as :$PORT when API_ADDR is unset (Render sets it)
//	COLLECT_WINDOW  24h   how far back a triggered collection looks
//	COLLECT_COOLDOWN 0    minimum time between triggered collections (429 inside it)
//	SOURCE_TIMEOUT  30s   per-source fetch timeout
//	HORIZON_ADMIN_TOKEN   bearer token required by every write (POST/PUT/PATCH/DELETE)
//	ADMIN_USERNAME  login user for the dashboard (POST /api/auth/login)
//	ADMIN_PASSWORD_HASH  bcrypt hash of the password (ADMIN_PASSWORD, plain,
//	                is accepted as a fallback and hashed at startup)
//	SESSION_SECRET  HMAC key of the 12 h session tokens (random per boot if unset)
//	RESEND_API_KEY, RESET_EMAIL_TO, FRONT_URL  enable "esqueci minha senha":
//	                a 30 min single-use link to RESET_EMAIL_TO only
//	RESET_FROM      sender (default "Horizon <onboarding@resend.dev>")
//	HORIZON_ENV     "production" (or running on Render) refuses writes while
//	                HORIZON_ADMIN_TOKEN is unset (fail closed)
//
// Triggered collections publish to NATS items.raw; the worker persists, scores
// and dedups them.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // America/Sao_Paulo without a system zoneinfo

	"golang.org/x/crypto/bcrypt"

	"github.com/jpdev/horizon-go/internal/adapters/bus/natsbus"
	"github.com/jpdev/horizon-go/internal/adapters/httpapi"
	"github.com/jpdev/horizon-go/internal/adapters/mail/resend"
	"github.com/jpdev/horizon-go/internal/adapters/source"
	"github.com/jpdev/horizon-go/internal/adapters/store/postgres"
	"github.com/jpdev/horizon-go/internal/app"
	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/internal/digest"
	"github.com/jpdev/horizon-go/internal/domain"
	"github.com/jpdev/horizon-go/internal/ingest"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// defaultCORSOrigins are the horizon-web (separate repo) Vite dev and preview
// servers.
const defaultCORSOrigins = "http://localhost:5173,http://localhost:4173,http://127.0.0.1:5173,http://127.0.0.1:4173"

// corsOrigins reads HORIZON_CORS_ORIGINS (or the older CORS_ORIGINS) as a
// comma-separated list, dropping blanks and trailing slashes.
func corsOrigins(lookup func(string) string) []string {
	raw := lookup("HORIZON_CORS_ORIGINS")
	if strings.TrimSpace(raw) == "" {
		raw = lookup("CORS_ORIGINS")
	}
	if strings.TrimSpace(raw) == "" {
		raw = defaultCORSOrigins
	}
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// listenAddr is API_ADDR, else ":$PORT" (Render, Fly and Heroku-style hosts
// set PORT), else :8080.
func listenAddr(lookup func(string) string) string {
	if v := strings.TrimSpace(lookup("API_ADDR")); v != "" {
		return v
	}
	if p := strings.TrimSpace(lookup("PORT")); p != "" {
		return ":" + p
	}
	return ":8080"
}

// loginFromEnv builds the dashboard login, or nil when ADMIN_USERNAME or a
// password is missing.
func loginFromEnv(lookup func(string) string, log *slog.Logger) (*httpapi.Login, error) {
	user := strings.TrimSpace(lookup("ADMIN_USERNAME"))
	hash := strings.TrimSpace(lookup("ADMIN_PASSWORD_HASH"))
	plain := lookup("ADMIN_PASSWORD")
	if user == "" || (hash == "" && plain == "") {
		return nil, nil
	}
	if hash == "" {
		h, err := bcrypt.GenerateFromPassword([]byte(plain), 12)
		if err != nil {
			return nil, fmt.Errorf("hash ADMIN_PASSWORD: %w", err)
		}
		hash = string(h)
	} else if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return nil, fmt.Errorf("ADMIN_PASSWORD_HASH is not a bcrypt hash: %w", err)
	}
	secret := []byte(lookup("SESSION_SECRET"))
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		log.Warn("SESSION_SECRET unset: sessions end when the API restarts")
	} else if len(secret) < 32 {
		return nil, errors.New("SESSION_SECRET must have at least 32 characters")
	}
	return &httpapi.Login{
		Username:     user,
		PasswordHash: []byte(hash),
		Sessions:     &httpapi.Sessions{Secret: secret, TTL: httpapi.DefaultSessionTTL},
		Limiter:      &httpapi.LoginLimiter{Max: 5, Window: time.Minute, MaxBlock: 15 * time.Minute},
	}, nil
}

// resetFromEnv enables "esqueci minha senha" when RESEND_API_KEY,
// RESET_EMAIL_TO and FRONT_URL are set. The link only ever goes to
// RESET_EMAIL_TO.
func resetFromEnv(lookup func(string) string) *httpapi.PasswordReset {
	key := strings.TrimSpace(lookup("RESEND_API_KEY"))
	to := strings.TrimSpace(lookup("RESET_EMAIL_TO"))
	front := strings.TrimSpace(lookup("FRONT_URL"))
	if key == "" || to == "" || front == "" {
		return nil
	}
	from := strings.TrimSpace(lookup("RESET_FROM"))
	if from == "" {
		from = "Horizon <onboarding@resend.dev>"
	}
	return &httpapi.PasswordReset{
		Mailer:     &resend.Client{APIKey: key, From: from},
		To:         to,
		FrontURL:   front,
		TTL:        httpapi.DefaultResetTTL,
		Limiter:    &httpapi.LoginLimiter{Max: 3, Window: 15 * time.Minute, MaxBlock: time.Hour},
		MaxPerHour: 5,
	}
}

// isProduction is HORIZON_ENV=production, or a Render service (RENDER is
// set on every Render instance).
func isProduction(lookup func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(lookup("HORIZON_ENV")), "production") ||
		strings.TrimSpace(lookup("RENDER")) != ""
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil && d > 0 {
		return d
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("api failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	pool, err := postgres.Connect(ctx, config.DatabaseURL())
	if err != nil {
		return err
	}
	defer pool.Close()
	bus, err := natsbus.Connect(ctx, natsbus.Config{URL: getenv("NATS_URL", "nats://localhost:4230")})
	if err != nil {
		return err
	}
	defer bus.Close()

	login, err := loginFromEnv(os.Getenv, log)
	if err != nil {
		return err
	}
	if login != nil {
		login.Creds = postgres.CredentialRepo{DB: pool}
		if err := login.Load(ctx); err != nil {
			log.Warn("admin credential: database unavailable, using ADMIN_PASSWORD_HASH", "err", err)
		}
	}
	reset := resetFromEnv(os.Getenv)
	timeout := envDuration("SOURCE_TIMEOUT", app.DefaultSourceTimeout)
	read := postgres.ReadRepo{DB: pool}
	sources := postgres.SourceRepo{DB: pool}
	collector := &app.Collector{Publisher: bus, Runs: postgres.FetchRunRepo{DB: pool}, Timeout: timeout, Log: log}
	srv := &httpapi.Server{
		Store:   read,
		Sources: sources,
		Digests: &digest.Service{Store: read, Failures: read},
		Alerts:  read,
		Collector: &ingest.Pipeline{
			Sources:   sources,
			Collector: collector,
			Client:    &http.Client{Timeout: timeout},
			Log:       log,
		},
		Pusher: &ingest.Pusher{Sources: sources, Items: postgres.ItemRepo{DB: pool}, Publisher: bus},
		ValidateSource: func(c domain.SourceConfig) error {
			_, err := source.Build(c, nil, source.BaseURLs{})
			return err
		},
		ValidateCron:    digest.ValidateCron,
		Checks:          []httpapi.Check{{Name: "postgres", Fn: read.Ping}, {Name: "nats", Fn: bus.Ping}},
		Window:          envDuration("COLLECT_WINDOW", 24*time.Hour),
		AllowedOrigins:  corsOrigins(os.Getenv),
		CollectCooldown: envDuration("COLLECT_COOLDOWN", 0),
		AdminToken:      strings.TrimSpace(os.Getenv("HORIZON_ADMIN_TOKEN")),
		Login:           login,
		Reset:           reset,
		RequireAdmin:    isProduction(os.Getenv),
		Log:             log,
	}
	hs := &http.Server{
		Addr:              listenAddr(os.Getenv),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      timeout + 2*time.Minute, // a triggered collection waits for its sources
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", hs.Addr, "cors", srv.AllowedOrigins,
			"admin_token", srv.AdminToken != "", "login", srv.Login.Enabled(), "password_reset", srv.Reset.Enabled(), "writes_open", srv.WritesOpen())
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
