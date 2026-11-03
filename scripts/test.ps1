# Static checks + unit tests for backend, typecheck + build for web.
. "$PSScriptRoot\_env.ps1"
Push-Location "$Root\backend"
try {
    Invoke-Step 'go build' { go build ./... }
    Invoke-Step 'go vet'   { go vet ./... }
    Invoke-Step 'go test'  { go test ./... }
}
finally { Pop-Location }
Push-Location "$Root\web"
try { Invoke-Step 'web build' { npm run build } }
finally { Pop-Location }
