# Applies migrations and runs cmd/api on :8080 in the foreground.
# Run cmd/worker in another terminal (cd backend; go run ./cmd/worker). The
# dashboard is a separate repo (horizon-web): its Vite server proxies /api here.
. "$PSScriptRoot\_env.ps1"
& "$PSScriptRoot\migrate.ps1" up
Push-Location "$Root\backend"
try { Invoke-Step 'api' { go run ./cmd/api } }
finally { Pop-Location }
