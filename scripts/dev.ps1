# Applies migrations and starts the Vite dev server.
# The API/worker binaries are stubs until Phases 1-3.
. "$PSScriptRoot\_env.ps1"
& "$PSScriptRoot\migrate.ps1" up
Push-Location "$Root\web"
try { Invoke-Step 'web dev' { npm run dev } }
finally { Pop-Location }
