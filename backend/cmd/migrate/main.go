// Command migrate applies the embedded goose migrations.
//
// Usage: migrate [up|down|status|version|reset] (default: up)
// DSN comes from DATABASE_URL (or the PG* parts; see internal/config).
package main

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jpdev/horizon-go/internal/config"
	"github.com/jpdev/horizon-go/migrations"
)

func main() {
	dsn := config.DatabaseURL()
	cmd := "up"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := migrations.Run(db, cmd); err != nil {
		log.Fatalf("goose %s: %v", cmd, err)
	}
}
