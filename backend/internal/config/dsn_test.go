package config

import (
	"net/url"
	"testing"
)

func clearPG(t *testing.T) {
	for _, k := range []string{"DATABASE_URL", "PGUSER", "PGPASSWORD", "PGHOST", "PGPORT", "PGDATABASE"} {
		t.Setenv(k, "")
	}
}

func TestDatabaseURLPrefersEnv(t *testing.T) {
	clearPG(t)
	t.Setenv("DATABASE_URL", "postgres://u@db:1/x")
	if got := DatabaseURL(); got != "postgres://u@db:1/x" {
		t.Fatalf("got %q", got)
	}
}

func TestDatabaseURLDefaultsToLocalDevContainer(t *testing.T) {
	clearPG(t)
	u, err := url.Parse(DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	if u.Scheme != "postgres" || u.User.Username() != "horizon" || pw != "horizon" ||
		u.Host != "localhost:5450" || u.Path != "/horizon" || u.Query().Get("sslmode") != "disable" {
		t.Fatalf("unexpected default DSN %q", u.String())
	}
}

func TestDatabaseURLBuiltFromParts(t *testing.T) {
	clearPG(t)
	t.Setenv("PGUSER", "app")
	t.Setenv("PGPASSWORD", "p@ss:w/rd")
	t.Setenv("PGHOST", "db.internal")
	t.Setenv("PGPORT", "6543")
	t.Setenv("PGDATABASE", "news")
	u, err := url.Parse(DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	if u.User.Username() != "app" || pw != "p@ss:w/rd" || u.Host != "db.internal:6543" || u.Path != "/news" {
		t.Fatalf("unexpected DSN %q", u.String())
	}
}

func TestDatabaseURLDropsNeonChannelBinding(t *testing.T) {
	clearPG(t)
	t.Setenv("DATABASE_URL", "postgresql://u:p@ep-x-123.sa-east-1.aws.neon.tech/neondb?sslmode=require&channel_binding=require")
	u, err := url.Parse(DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Has("channel_binding") || u.Query().Get("sslmode") != "require" || u.Host != "ep-x-123.sa-east-1.aws.neon.tech" {
		t.Fatalf("got %q", u.String())
	}
}
