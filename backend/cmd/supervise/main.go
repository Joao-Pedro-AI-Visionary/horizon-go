// Command supervise runs the whole backend in one container (the Render free
// web service, see docs/deploy.md):
//
//  1. nats-server with JetStream (store in /tmp: lost on restart or spin-down)
//  2. the goose migrations (embedded), retried while the database wakes up
//  3. the api (on $PORT) and the worker, as child processes
//
// If any child exits, the others get SIGTERM (SIGKILL after a grace period)
// and supervise exits non-zero, so the platform restarts the container.
// SIGTERM/SIGINT to supervise stops everything and exits 0.
//
// Environment (all optional):
//
//	HORIZON_BIN_DIR    directory with api, worker (default: next to this binary)
//	NATS_SERVER_BIN    nats-server path (default: nats-server on PATH)
//	NATS_CONFIG        nats-server config file (default /etc/nats/nats.conf)
//	NATS_URL           when set, no embedded nats-server is started
//	MIGRATE_ON_BOOT    true (default) | false
//	WORKER_SEED        true (default) | false: worker --seed (idempotent; default sources)
//	WORKER_ARGS        extra worker flags, space separated
//	API_GOMEMLIMIT, WORKER_GOMEMLIMIT, NATS_GOMEMLIMIT  per-process Go soft memory limits
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/migrations"
)

const (
	embeddedNATS = "nats://127.0.0.1:4222"
	grace        = 15 * time.Second
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil)).With("proc", "supervise")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := run(ctx, log)
	if err != nil {
		log.Error("supervise failed", "err", err)
	}
	os.Exit(code)
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func enabled(k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "":
		return def
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func run(ctx context.Context, log *slog.Logger) (int, error) {
	binDir := getenv("HORIZON_BIN_DIR", "")
	if binDir == "" {
		exe, err := os.Executable()
		if err != nil {
			return 1, err
		}
		binDir = filepath.Dir(exe)
	}
	env := os.Environ()
	var specs []spec

	if os.Getenv("NATS_URL") == "" {
		specs = append(specs, spec{name: "nats", path: getenv("NATS_SERVER_BIN", "nats-server"),
			args: []string{"-c", getenv("NATS_CONFIG", "/etc/nats/nats.conf")}, memLimit: os.Getenv("NATS_GOMEMLIMIT")})
		env = setEnv(env, "NATS_URL", embeddedNATS)
	}
	// Render (and similar hosts) give the port in PORT.
	if os.Getenv("API_ADDR") == "" {
		env = setEnv(env, "API_ADDR", ":"+getenv("PORT", "8080"))
	}

	sup := &supervisor{log: log, env: env}
	defer sup.stopAll()

	if len(specs) > 0 {
		if err := sup.start(specs[0]); err != nil {
			return 1, err
		}
		if err := waitTCP(ctx, "127.0.0.1:4222", 20*time.Second, sup.exited); err != nil {
			return 1, fmt.Errorf("nats-server: %w", err)
		}
		log.Info("nats ready", "url", embeddedNATS)
	}

	if enabled("MIGRATE_ON_BOOT", true) {
		if err := migrate(ctx, log); err != nil {
			return 1, err
		}
	}

	workerArgs := []string{}
	if enabled("WORKER_SEED", true) {
		workerArgs = append(workerArgs, "--seed")
	}
	workerArgs = append(workerArgs, strings.Fields(os.Getenv("WORKER_ARGS"))...)
	for _, s := range []spec{
		{name: "worker", path: filepath.Join(binDir, "worker"), args: workerArgs, memLimit: os.Getenv("WORKER_GOMEMLIMIT")},
		{name: "api", path: filepath.Join(binDir, "api"), memLimit: os.Getenv("API_GOMEMLIMIT")},
	} {
		if err := sup.start(s); err != nil {
			return 1, err
		}
	}
	return sup.wait(ctx)
}

// migrate applies the embedded migrations; Neon may need a few seconds to
// wake a suspended compute, so connection errors are retried.
func migrate(ctx context.Context, log *slog.Logger) error {
	db, err := sql.Open("pgx", config.DatabaseURL())
	if err != nil {
		return err
	}
	defer db.Close()
	var last error
	for attempt := 1; attempt <= 6; attempt++ {
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		last = db.PingContext(pctx)
		cancel()
		if last == nil {
			break
		}
		log.Warn("database not ready", "attempt", attempt, "err", last)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 2 * time.Second):
		}
	}
	if last != nil {
		return fmt.Errorf("database unreachable: %w", last)
	}
	start := time.Now()
	if err := migrations.Run(db, "up"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	log.Info("migrations applied", "took", time.Since(start).Round(time.Millisecond))
	return nil
}

func setEnv(env []string, k, v string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, k+"=") {
			out = append(out, kv)
		}
	}
	return append(out, k+"="+v)
}

type spec struct {
	name, path string
	args       []string
	memLimit   string
}

type exit struct {
	name string
	err  error
}

type supervisor struct {
	log    *slog.Logger
	env    []string
	procs  []*exec.Cmd
	names  []string
	exited chan exit
}

func (s *supervisor) start(sp spec) error {
	if s.exited == nil {
		s.exited = make(chan exit, 8)
	}
	cmd := exec.Command(sp.path, sp.args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = s.env
	if sp.memLimit != "" {
		cmd.Env = setEnv(cmd.Env, "GOMEMLIMIT", sp.memLimit)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", sp.name, err)
	}
	s.log.Info("started", "child", sp.name, "pid", cmd.Process.Pid)
	s.procs = append(s.procs, cmd)
	s.names = append(s.names, sp.name)
	go func() { s.exited <- exit{sp.name, cmd.Wait()} }()
	return nil
}

// wait returns when a child exits (code 1) or ctx is cancelled (code 0).
func (s *supervisor) wait(ctx context.Context) (int, error) {
	select {
	case e := <-s.exited:
		s.markDone(e.name)
		if e.err == nil {
			e.err = errors.New("exited")
		}
		return 1, fmt.Errorf("%s stopped: %w", e.name, e.err)
	case <-ctx.Done():
		s.log.Info("shutting down")
		return 0, nil
	}
}

func (s *supervisor) markDone(name string) {
	for i, n := range s.names {
		if n == name {
			s.procs[i] = nil
		}
	}
}

// stopAll stops the api and worker first and nats-server last (so they can
// still ack in-flight messages): SIGTERM, then SIGKILL after the grace period.
func (s *supervisor) stopAll() {
	s.stopWhere(func(name string) bool { return name != "nats" })
	s.stopWhere(func(name string) bool { return name == "nats" })
}

func (s *supervisor) stopWhere(match func(string) bool) {
	alive := map[string]bool{}
	for i, p := range s.procs {
		if p != nil && match(s.names[i]) {
			_ = p.Process.Signal(syscall.SIGTERM)
			alive[s.names[i]] = true
		}
	}
	deadline := time.After(grace)
	for len(alive) > 0 {
		select {
		case e := <-s.exited:
			s.markDone(e.name)
			s.log.Info("stopped", "child", e.name)
			delete(alive, e.name)
		case <-deadline:
			for i, p := range s.procs {
				if p != nil && alive[s.names[i]] {
					s.log.Warn("killing", "child", s.names[i])
					_ = p.Process.Kill()
				}
			}
			return
		}
	}
}

// waitTCP polls addr until it accepts connections, a child dies or timeout.
func waitTCP(ctx context.Context, addr string, timeout time.Duration, exited chan exit) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-exited:
			exited <- e // let wait/stopAll see it too
			return fmt.Errorf("%s exited before listening: %v", e.name, e.err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s not listening after %s", addr, timeout)
}
