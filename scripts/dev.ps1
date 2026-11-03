# Applies migrations and starts the Vite dev server.
# Run cmd/api and cmd/worker separately; Vite proxies /api to :8080.
. "$PSScriptRoot\_env.ps1"
& "$PSScriptRoot\migrate.ps1" up
Push-Location "$Root\web"
try { Invoke-Step 'web dev' { npm run dev } }
finally { Pop-Location }
