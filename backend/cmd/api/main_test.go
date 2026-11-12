package main

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestCORSOrigins(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"default", nil, []string{"http://localhost:5173", "http://localhost:4173", "http://127.0.0.1:5173", "http://127.0.0.1:4173"}},
		{"horizon var", map[string]string{"HORIZON_CORS_ORIGINS": " https://web.example/ , ,http://localhost:3000"}, []string{"https://web.example", "http://localhost:3000"}},
		{"legacy var", map[string]string{"CORS_ORIGINS": "https://old.example"}, []string{"https://old.example"}},
		{"horizon wins", map[string]string{"HORIZON_CORS_ORIGINS": "https://new.example", "CORS_ORIGINS": "https://old.example"}, []string{"https://new.example"}},
		{"blank falls back", map[string]string{"HORIZON_CORS_ORIGINS": "  "}, []string{"http://localhost:5173", "http://localhost:4173", "http://127.0.0.1:5173", "http://127.0.0.1:4173"}},
	}
	for _, c := range cases {
		if got := corsOrigins(env(c.env)); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestListenAddr(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{nil, ":8080"},
		{map[string]string{"PORT": "10000"}, ":10000"},
		{map[string]string{"PORT": "10000", "API_ADDR": "127.0.0.1:9000"}, "127.0.0.1:9000"},
	} {
		if got := listenAddr(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: got %q want %q", c.env, got, c.want)
		}
	}
}

func TestIsProduction(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"HORIZON_ENV": "development"}, false},
		{map[string]string{"HORIZON_ENV": "Production"}, true},
		{map[string]string{"RENDER": "true"}, true},
	} {
		if got := isProduction(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: got %v want %v", c.env, got, c.want)
		}
	}
}

func TestLoginFromEnv(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if l, err := loginFromEnv(env(nil), log); l != nil || err != nil {
		t.Fatalf("unset: %v %v", l, err)
	}
	l, err := loginFromEnv(env(map[string]string{"ADMIN_USERNAME": "joao", "ADMIN_PASSWORD": "a long enough password", "SESSION_SECRET": strings.Repeat("s", 32)}), log)
	if err != nil || !l.Enabled() || !l.Check("joao", "a long enough password") || l.Check("joao", "nope") {
		t.Fatalf("plain fallback: %v", err)
	}
	if _, err := loginFromEnv(env(map[string]string{"ADMIN_USERNAME": "joao", "ADMIN_PASSWORD_HASH": "not-bcrypt"}), log); err == nil {
		t.Fatal("bad hash accepted")
	}
	if _, err := loginFromEnv(env(map[string]string{"ADMIN_USERNAME": "joao", "ADMIN_PASSWORD": "x", "SESSION_SECRET": "short"}), log); err == nil {
		t.Fatal("short secret accepted")
	}
}
