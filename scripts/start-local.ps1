# Starts the Horizon backend locally, detached from the calling shell: the
# containers (docker in WSL), migrations, cmd/api on http://localhost:8080 and
# cmd/worker. The dashboard is a separate repo (horizon-web,
# https://github.com/Joao-Pedro-AI-Visionary/horizon-web): run its
# `npm run dev` (:5173) or `npm run preview` (:4173) against this API.
# Processes are created through WMI (Win32_Process.Create), so they are not
# children of this shell and survive when the terminal or agent session ends.
# PIDs go to .runs/local-pids.json; stop them with scripts\stop-local.ps1.
#   .\scripts\start-local.ps1
. "$PSScriptRoot\_env.ps1"
$bin = "$Root\bin"
$runs = "$Root\.runs"
$pidFile = "$runs\local-pids.json"
New-Item -ItemType Directory -Force $bin, $runs | Out-Null

if (Test-Path $pidFile) { throw "$pidFile exists: run scripts\stop-local.ps1 first" }
if (Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue) {
    throw 'port 8080 is already in use'
}

Invoke-Step 'containers' { wsl docker start horizon-pg horizon-nats }
& "$PSScriptRoot\migrate.ps1" up
Push-Location "$Root\backend"
try {
    Invoke-Step 'build api'    { go build -o "$bin\api.exe" ./cmd/api }
    Invoke-Step 'build worker' { go build -o "$bin\worker.exe" ./cmd/worker }
}
finally { Pop-Location }
# detached runs `cmd /c "<command> > log 2>&1"` through WMI and returns the
# wrapper cmd.exe and the real process (its child) with their start times.
function Start-Detached([string]$Name, [string]$Command, [string]$Dir) {
    $log = "$runs\local-$Name.log"
    $line = "cmd.exe /c `"$Command > `"$log`" 2>&1`""
    $r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $line; CurrentDirectory = $Dir }
    if ($r.ReturnValue -ne 0) { throw "Win32_Process.Create failed for $Name ($($r.ReturnValue))" }
    $wrapper = [int]$r.ProcessId
    $child = $null
    for ($i = 0; $i -lt 40 -and -not $child; $i++) {
        Start-Sleep -Milliseconds 250
        $child = Get-CimInstance Win32_Process -Filter "ParentProcessId=$wrapper" | Where-Object Name -ne 'conhost.exe' | Select-Object -First 1
    }
    if (-not $child) { throw "$Name did not start (see $log)" }
    $wp = Get-CimInstance Win32_Process -Filter "ProcessId=$wrapper"
    [pscustomobject]@{
        name         = $Name
        pid          = [int]$child.ProcessId
        exe          = $child.Name
        started      = $child.CreationDate.ToString('o')
        wrapperPid   = $wrapper
        wrapperStart = $wp.CreationDate.ToString('o')
        log          = $log
    }
}

$procs = @(
    (Start-Detached 'api' "`"$bin\api.exe`"" $Root),
    (Start-Detached 'worker' "`"$bin\worker.exe`"" $Root)
)
$procs | ConvertTo-Json | Set-Content -Encoding utf8 $pidFile

$deadline = (Get-Date).AddSeconds(60)
while ($true) {
    try { if ((Invoke-WebRequest -UseBasicParsing http://localhost:8080/healthz).StatusCode -eq 200) { break } } catch {}
    if ((Get-Date) -gt $deadline) { throw 'http://localhost:8080/healthz did not answer within 60s (logs in .runs\)' }
    Start-Sleep -Milliseconds 500
}
$procs | Format-Table name, pid, exe, wrapperPid -AutoSize
Write-Host 'Horizon API: http://localhost:8080  (dashboard: horizon-web)  stop: .\scripts\stop-local.ps1' -ForegroundColor Yellow
