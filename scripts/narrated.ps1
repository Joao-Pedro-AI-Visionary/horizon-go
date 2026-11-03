# Narrated pt-BR video of the dashboard (videos/narrated/, script in
# videos/narrated/script.md). Generates the narration (edge-tts), builds and
# starts api + worker + vite preview (Postgres, NATS and Ollama must be up),
# records narrated/tour.spec.ts at 1920x1080 against the real backend, muxes
# the 16:9, 9:16 and 1:1 MP4s, and stops only the PIDs it started.
#   .\scripts\narrated.ps1              # tts + recording + videos
#   .\scripts\narrated.ps1 -SkipTts     # reuse videos/narrated/work/*.mp3
param([switch]$SkipTts)
. "$PSScriptRoot\_env.ps1"
$bin = "$Root\bin"
$web = "$Root\web"
New-Item -ItemType Directory -Force $bin | Out-Null

foreach ($port in 8080, 4173) {
    if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
        throw "port $port is already in use: stop the running stack first (scripts\stop-local.ps1)"
    }
}

Push-Location $web
try {
    if (-not $SkipTts) { Invoke-Step 'narration (edge-tts)' { node scripts/narrated-tts.mjs } }
    Invoke-Step 'build web' { npm run build }
}
finally { Pop-Location }

& "$PSScriptRoot\migrate.ps1" up
Push-Location "$Root\backend"
try {
    Invoke-Step 'build api'    { go build -o "$bin\api.exe" ./cmd/api }
    Invoke-Step 'build worker' { go build -o "$bin\worker.exe" ./cmd/worker }
}
finally { Pop-Location }

$started = @()
try {
    $api = Start-Process "$bin\api.exe" -PassThru -NoNewWindow `
        -RedirectStandardOutput "$bin\narr-api.log" -RedirectStandardError "$bin\narr-api.err.log"
    $started += $api
    $worker = Start-Process "$bin\worker.exe" -PassThru -NoNewWindow `
        -RedirectStandardOutput "$bin\narr-worker.log" -RedirectStandardError "$bin\narr-worker.err.log"
    $started += $worker
    $preview = Start-Process node -ArgumentList 'node_modules/vite/bin/vite.js', 'preview', '--port', '4173', '--strictPort' `
        -WorkingDirectory $web -PassThru -NoNewWindow `
        -RedirectStandardOutput "$bin\narr-preview.log" -RedirectStandardError "$bin\narr-preview.err.log"
    $started += $preview
    Write-Host "api PID $($api.Id), worker PID $($worker.Id), preview PID $($preview.Id)"

    $deadline = (Get-Date).AddSeconds(60)
    while ($true) {
        try {
            if ((Invoke-WebRequest -UseBasicParsing http://localhost:4173/healthz).StatusCode -eq 200) { break }
        } catch {}
        if ((Get-Date) -gt $deadline) { throw 'api/preview did not answer within 60s' }
        Start-Sleep -Milliseconds 500
    }

    Push-Location $web
    try {
        $env:E2E_BASE_URL = 'http://localhost:4173'
        Invoke-Step 'narrated tour' { npx playwright test --config playwright.narrated.config.ts }
        Invoke-Step 'narrated videos' { node scripts/narrated-video.mjs }
    }
    finally { Remove-Item Env:E2E_BASE_URL -ErrorAction SilentlyContinue; Pop-Location }
}
finally {
    foreach ($p in $started) {
        if (-not $p.HasExited) { Stop-Process -Id $p.Id -Force; Write-Host "stopped PID $($p.Id)" }
    }
}
