// Package config resolves runtime settings shared by the commands and the
// integration tests.
package config

import (
	"net"
	"net/url"
	"os"
)

// Local development defaults. They match the docker-compose.yml Postgres
// container (see .env.example); they are not production credentials.
const (
	defaultPGUser     = "horizon"
	defaultPGPassword = "horizon"
	defaultPGHost     = "localhost"
	defaultPGPort     = "5450"
	defaultPGDatabase = "horizon"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// DatabaseURL returns DATABASE_URL when set; otherwise it builds the DSN from
// PGUSER, PGPASSWORD, PGHOST, PGPORT and PGDATABASE, each falling back to the
// local development container defaults.
func DatabaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(getenv("PGUSER", defaultPGUser), getenv("PGPASSWORD", defaultPGPassword)),
		Host:     net.JoinHostPort(getenv("PGHOST", defaultPGHost), getenv("PGPORT", defaultPGPort)),
		Path:     "/" + getenv("PGDATABASE", defaultPGDatabase),
		RawQuery: "sslmode=disable",
	}
	return u.String()
}
