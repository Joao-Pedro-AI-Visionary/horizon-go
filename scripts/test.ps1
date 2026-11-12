# Static checks + unit tests for the backend. The dashboard's build and e2e
# live in horizon-web (https://github.com/Joao-Pedro-AI-Visionary/horizon-web).
. "$PSScriptRoot\_env.ps1"
Push-Location "$Root\backend"
try {
    Invoke-Step 'go build' { go build ./... }
    Invoke-Step 'go vet'   { go vet ./... }
    Invoke-Step 'go test'  { go test ./... }
}
finally { Pop-Location }
