# Unix-like alternative to scripts/*.ps1 (Windows users: use the PowerShell scripts).
GO ?= go
# DATABASE_URL (or PGUSER/PGPASSWORD/PGHOST/PGPORT/PGDATABASE) is optional:
# the Go commands default to the local dev container. See .env.example.

.PHONY: migrate dev test e2e

migrate:
	cd backend && $(GO) run ./cmd/migrate up

dev: migrate
	cd web && npm run dev

test:
	cd backend && $(GO) build ./... && $(GO) vet ./... && $(GO) test ./...
	cd web && npm run build

e2e:
	cd web && npx playwright install chromium && npm run e2e
