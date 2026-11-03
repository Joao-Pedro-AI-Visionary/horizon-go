# Playwright e2e in desktop/tablet/mobile viewports (builds and serves web via vite preview).
. "$PSScriptRoot\_env.ps1"
Push-Location "$Root\web"
try {
    Invoke-Step 'playwright chromium' { npx playwright install chromium }
    Invoke-Step 'e2e' { npm run e2e }
}
finally { Pop-Location }
