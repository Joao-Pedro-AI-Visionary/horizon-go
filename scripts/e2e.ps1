# Playwright e2e against the real backend in desktop/tablet/mobile viewports.
# Builds cmd/api and cmd/worker, starts both (Postgres, NATS and Ollama must be
# up), runs the suite (vite preview is started by playwright.config.ts), turns
# the recordings into videos/e2e-<project>.mp4 and stops api + worker by PID.
#   .\scripts\e2e.ps1            # suite + videos
#   .\scripts\e2e.ps1 -Demo      # also records videos/demo-final.mp4
#   .\scripts\e2e.ps1 -NoVideos  # suite only
param([switch]$Demo, [switch]$NoVideos)
. "$PSScriptRoot\_env.ps1"
$bin = "$Root\bin"
New-Item -ItemType Directory -Force $bin | Out-Null

if (Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue) {
    throw 'port 8080 is already in use: stop the running API first'
}

& "$PSScriptRoot\migrate.ps1" up
Push-Location "$Root\backend"
try {
    Invoke-Step 'build api'    { go build -o "$bin\api.exe" ./cmd/api }
    Invoke-Step 'build worker' { go build -o "$bin\worker.exe" ./cmd/worker }
}
finally { Pop-Location }

$started = @()
$suiteExit = 1
try {
    $api = Start-Process "$bin\api.exe" -PassThru -NoNewWindow `
        -RedirectStandardOutput "$bin\e2e-api.log" -RedirectStandardError "$bin\e2e-api.err.log"
    $started += $api
    $worker = Start-Process "$bin\worker.exe" -PassThru -NoNewWindow `
        -RedirectStandardOutput "$bin\e2e-worker.log" -RedirectStandardError "$bin\e2e-worker.err.log"
    $started += $worker
    Write-Host "api PID $($api.Id), worker PID $($worker.Id)"

    $deadline = (Get-Date).AddSeconds(60)
    while ($true) {
        try { if ((Invoke-WebRequest -UseBasicParsing http://localhost:8080/healthz).StatusCode -eq 200) { break } } catch {}
        if ((Get-Date) -gt $deadline) { throw 'api did not answer /healthz within 60s' }
        Start-Sleep -Milliseconds 500
    }

    Push-Location "$Root\web"
    try {
        Invoke-Step 'playwright chromium' { npx playwright install chromium }
        Write-Host '==> e2e' -ForegroundColor Yellow
        npx playwright test
        $suiteExit = $LASTEXITCODE
        if (-not $NoVideos) { Invoke-Step 'e2e videos' { node scripts/videos.mjs } }
        if ($Demo) {
            Invoke-Step 'demo tour' { npx playwright test --config playwright.demo.config.ts }
            Invoke-Step 'demo video' { node scripts/demo-video.mjs }
        }
    }
    finally { Pop-Location }
}
finally {
    foreach ($p in $started) {
        if (-not $p.HasExited) { Stop-Process -Id $p.Id -Force; Write-Host "stopped PID $($p.Id)" }
    }
}
if ($suiteExit -ne 0) { throw "e2e failed (exit $suiteExit)" }
