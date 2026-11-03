// Package migrations embeds the goose SQL migrations so binaries can apply them.
package migrations

import (
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

// Run executes a goose command (up, down, status, ...) with the embedded files.
func Run(db *sql.DB, cmd string) error {
	goose.SetBaseFS(FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Run(cmd, db, ".")
}
