# Unix-like alternative to scripts/*.ps1 (Windows users: use the PowerShell scripts).
# Backend only: the dashboard lives in
# https://github.com/Joao-Pedro-AI-Visionary/horizon-web.
GO ?= go
# DATABASE_URL (or PGUSER/PGPASSWORD/PGHOST/PGPORT/PGDATABASE) is optional:
# the Go commands default to the local dev container. See .env.example.

.PHONY: migrate dev worker test

migrate:
	cd backend && $(GO) run ./cmd/migrate up

# API on :8080 (run `make worker` in another terminal).
dev: migrate
	cd backend && $(GO) run ./cmd/api

worker:
	cd backend && $(GO) run ./cmd/worker

test:
	cd backend && $(GO) build ./... && $(GO) vet ./... && $(GO) test ./...
