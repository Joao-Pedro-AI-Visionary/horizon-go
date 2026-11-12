package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestOneChildExitStopsTheOthers(t *testing.T) {
	s := &supervisor{log: quiet(), env: os.Environ()}
	if err := s.start(spec{name: "long", path: "sleep", args: []string{"30"}}); err != nil {
		t.Skip("no sleep binary:", err)
	}
	if err := s.start(spec{name: "dies", path: "sh", args: []string{"-c", "sleep 0.2; exit 3"}}); err != nil {
		t.Skip("no sh:", err)
	}
	start := time.Now()
	code, err := s.wait(context.Background())
	if code != 1 || err == nil || !strings.Contains(err.Error(), "dies") {
		t.Fatalf("code=%d err=%v", code, err)
	}
	s.stopAll()
	if time.Since(start) > 5*time.Second {
		t.Errorf("stopping took %s", time.Since(start))
	}
	if p := s.procs[0]; p != nil && p.ProcessState == nil {
		t.Error("the long child is still running")
	}
}

func TestCancelStopsCleanly(t *testing.T) {
	s := &supervisor{log: quiet(), env: os.Environ()}
	if err := s.start(spec{name: "long", path: "sleep", args: []string{"30"}}); err != nil {
		t.Skip("no sleep binary:", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code, err := s.wait(ctx); code != 0 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	s.stopAll()
}

func TestSetEnvReplaces(t *testing.T) {
	env := setEnv([]string{"A=1", "NATS_URL=x", "B=2"}, "NATS_URL", "nats://127.0.0.1:4222")
	if strings.Join(env, ",") != "A=1,B=2,NATS_URL=nats://127.0.0.1:4222" {
		t.Errorf("env = %v", env)
	}
}

func TestEnabled(t *testing.T) {
	t.Setenv("X_FLAG", "false")
	if enabled("X_FLAG", true) {
		t.Error("false must disable")
	}
	t.Setenv("X_FLAG", "")
	if !enabled("X_FLAG", true) {
		t.Error("empty uses the default")
	}
}
