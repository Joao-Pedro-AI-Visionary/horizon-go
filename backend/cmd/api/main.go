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
//	COLLECT_INTERVAL 30m  worker interval; "off" enables the API's internal cron + catch-up scheduler
//	INTERNAL_SCHEDULER    optional override ("on"/"off") for the internal scheduler
//	TZ              America/Sao_Paulo  scheduler wall-clock timezone
//	SOURCE_TIMEOUT  30s   per-source fetch timeout
//	HORIZON_ADMIN_TOKEN   bearer token required by every write (POST/PUT/PATCH/DELETE)
//	ADMIN_USERNAME  login user for the dashboard (POST /api/auth/login)
//	ADMIN_PASSWORD_HASH  bcrypt hash of the password (ADMIN_PASSWORD, plain,
//	                is accepted as a fallback and hashed at startup)
//	SESSION_SECRET  HMAC key of the 12 h session tokens (random per boot if unset)
//	RESEND_API_KEY, RESET_EMAIL_TO, FRONT_URL  enable "esqueci minha senha":
//	                a 30 min single-use link to RESET_EMAIL_TO only. RESEND_API_KEY +
//	                RESET_EMAIL_TO alone also enable operational alert e-mails.
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
	"github.com/jpdev/horizon-go/internal/opsalerts"
	"github.com/jpdev/horizon-go/internal/retention"
	"github.com/jpdev/horizon-go/internal/scheduler"
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

// voteHashSecret returns the HMAC key for hashing voter device IDs and IPs.
func voteHashSecret(lookup func(string) string, log *slog.Logger) ([]byte, error) {
	for _, k := range []string{"VOTE_HASH_SECRET", "SESSION_SECRET", "HORIZON_ADMIN_TOKEN"} {
		if v := strings.TrimSpace(lookup(k)); v != "" {
			return []byte(v), nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	log.Warn("VOTE_HASH_SECRET unset: using ephemeral key for anonymous votes")
	return buf, nil
}

// mailerFromEnv returns the configured Resend client and admin recipient
// (RESET_EMAIL_TO) when RESEND_API_KEY and RESET_EMAIL_TO are set, using
// RESET_FROM (default "Horizon <onboarding@resend.dev>").
func mailerFromEnv(lookup func(string) string) (*resend.Client, string) {
	key := strings.TrimSpace(lookup("RESEND_API_KEY"))
	to := strings.TrimSpace(lookup("RESET_EMAIL_TO"))
	if key == "" || to == "" {
		return nil, ""
	}
	from := strings.TrimSpace(lookup("RESET_FROM"))
	if from == "" {
		from = "Horizon <onboarding@resend.dev>"
	}
	return &resend.Client{APIKey: key, From: from}, to
}

// resetFromEnv enables "esqueci minha senha" when RESEND_API_KEY,
// RESET_EMAIL_TO and FRONT_URL are set. The link only ever goes to
// RESET_EMAIL_TO.
func resetFromEnv(lookup func(string) string) *httpapi.PasswordReset {
	mailer, to := mailerFromEnv(lookup)
	front := strings.TrimSpace(lookup("FRONT_URL"))
	if mailer == nil || front == "" {
		return nil
	}
	return &httpapi.PasswordReset{
		Mailer:     mailer,
		To:         to,
		FrontURL:   front,
		TTL:        httpapi.DefaultResetTTL,
		Limiter:    &httpapi.LoginLimiter{Max: 3, Window: 15 * time.Minute, MaxBlock: time.Hour},
		MaxPerHour: 5,
	}
}

func opsAlertsFromEnv(lookup func(string) string, store opsalerts.Store, log *slog.Logger) *opsalerts.Service {
	var mailer opsalerts.Mailer
	if rc, to := mailerFromEnv(lookup); rc != nil {
		mailer = rc
		return &opsalerts.Service{
			Store:    store,
			Mailer:   mailer,
			To:       to,
			Cooldown: opsalerts.DefaultCooldown,
			Timezone: opsalerts.DefaultTimezone,
			Log:      log,
		}
	}
	return &opsalerts.Service{
		Store:    store,
		Cooldown: opsalerts.DefaultCooldown,
		Timezone: opsalerts.DefaultTimezone,
		Log:      log,
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
	schedEnabled, schedReason, err := config.ResolveInternalScheduler(os.Getenv)
	if err != nil {
		return err
	}
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
	voteSecret, err := voteHashSecret(os.Getenv, log)
	if err != nil {
		return err
	}
	timeout := envDuration("SOURCE_TIMEOUT", app.DefaultSourceTimeout)
	read := postgres.ReadRepo{DB: pool}
	sources := postgres.SourceRepo{DB: pool}
	votes := postgres.VoteRepo{DB: pool}
	schedRepo := postgres.SchedulerRepo{DB: pool}
	opsAlerts := opsAlertsFromEnv(os.Getenv, read, log)
	collector := &app.Collector{Publisher: bus, Runs: postgres.FetchRunRepo{DB: pool}, Timeout: timeout, Log: log}
	srv := &httpapi.Server{
		Store:       read,
		Sources:     sources,
		Votes:       votes,
		VoteHashSecret: voteSecret,
		Cleanup:     &retention.Service{Store: postgres.CleanupRepo{DB: pool}},
		Digests:     &digest.Service{Store: read, Failures: read},
		Alerts:      read,
		AlertTester: opsAlerts,
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
	if schedEnabled {
		srv.OnCollect = func(c context.Context, at time.Time) {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(c), 5*time.Second)
			defer rcancel()
			if err := schedRepo.RecordManualCollect(rctx, at); err != nil {
				log.Warn("scheduler: failed to persist collect timestamp", "err", err)
			}
		}
		if st, err := schedRepo.EnsureJob(ctx, scheduler.JobCollect, time.Now().UTC()); err != nil {
			log.Warn("scheduler: failed to load collect cooldown state", "err", err)
		} else if st.LastRunAt != nil {
			srv.SeedLastCollect(*st.LastRunAt)
		}
	}
	hs := &http.Server{
		Addr:              listenAddr(os.Getenv),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      timeout + 2*time.Minute, // a triggered collection waits for its sources
		IdleTimeout:       2 * time.Minute,
	}
	schedCtx, cancelSched := context.WithCancel(ctx)
	defer cancelSched()
	var schedDone chan error
	if schedEnabled {
		sched := &scheduler.Service{
			Store:           schedRepo,
			Collector:       srv,
			Cleaner:         srv.Cleanup,
			Alerter:         opsalerts.SchedulerBridge{Service: opsAlerts},
			XIngest:         read,
			CollectCooldown: srv.CollectCooldown,
			Log:             log,
		}
		schedDone = make(chan error, 1)
		log.Info("internal scheduler enabled", "reason", schedReason, "timezone", scheduler.Timezone)
		go func() {
			schedDone <- sched.Run(schedCtx)
		}()
	} else {
		log.Info("internal scheduler disabled", "reason", schedReason)
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", hs.Addr, "cors", srv.AllowedOrigins,
			"admin_token", srv.AdminToken != "", "login", srv.Login.Enabled(), "password_reset", srv.Reset.Enabled(), "ops_alerts_email", opsAlerts.Mailer != nil, "writes_open", srv.WritesOpen())
		errc <- hs.ListenAndServe()
	}()
	var exitErr error
	select {
	case err := <-errc:
		exitErr = err
	case <-ctx.Done():
	}
	cancelSched()
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) && exitErr == nil {
		exitErr = err
	}
	if schedDone != nil {
		select {
		case err := <-schedDone:
			if err != nil && exitErr == nil {
				exitErr = err
			}
		case <-time.After(10 * time.Second):
			log.Warn("internal scheduler did not stop within shutdown timeout")
		}
	}
	return exitErr
}
