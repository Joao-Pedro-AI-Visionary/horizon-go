# Stops what scripts\start-local.ps1 started, using only the PIDs saved in
# .runs/local-pids.json. A PID is stopped only if it still belongs to the same
# process (same executable and start time), so a reused PID is never killed.
# It never kills processes by name.
#   .\scripts\stop-local.ps1               # api, worker, vite preview
#   .\scripts\stop-local.ps1 -Containers   # also stops horizon-pg and horizon-nats
param([switch]$Containers)
$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
$pidFile = "$Root\.runs\local-pids.json"
$protected = @(22740) # never touched, whatever the file says

function Stop-Saved([int]$Id, [string]$Exe, [string]$Started) {
    if ($protected -contains $Id) { Write-Warning "PID $Id is protected: skipped"; return }
    $p = Get-CimInstance Win32_Process -Filter "ProcessId=$Id"
    if (-not $p) { Write-Host "PID $Id ($Exe) already gone"; return }
    if ($p.Name -ne $Exe -or $p.CreationDate.ToString('o') -ne $Started) {
        Write-Warning "PID $Id is now $($p.Name) started $($p.CreationDate): not ours, skipped"
        return
    }
    Stop-Process -Id $Id -Force
    Write-Host "stopped PID $Id ($Exe)"
}

if (Test-Path $pidFile) {
    # Assign first: in Windows PowerShell 5.1, @(... | ConvertFrom-Json) wraps the whole array as one item.
    $entries = Get-Content -Raw $pidFile | ConvertFrom-Json
    foreach ($e in $entries) {
        Stop-Saved $e.pid $e.exe $e.started
        Stop-Saved $e.wrapperPid 'cmd.exe' $e.wrapperStart
    }
    Remove-Item $pidFile
}
else { Write-Host "no $pidFile`: nothing to stop" }

if ($Containers) { wsl docker stop horizon-pg horizon-nats }
