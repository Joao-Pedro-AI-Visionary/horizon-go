# Applies goose migrations to DATABASE_URL (default: local dev container). Usage: .\scripts\migrate.ps1 [up|down|status]
param([string]$Command = 'up')
. "$PSScriptRoot\_env.ps1"
Push-Location "$Root\backend"
try { Invoke-Step "goose $Command" { go run ./cmd/migrate $Command } }
finally { Pop-Location }
