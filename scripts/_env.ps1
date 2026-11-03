# Shared settings for the dev scripts. Dot-source it: . "$PSScriptRoot\_env.ps1"
$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
$env:Path = "$env:LOCALAPPDATA\Programs\go\bin;$env:USERPROFILE\go\bin;$env:Path"
$env:GOTOOLCHAIN = 'local'
# DATABASE_URL is optional: the Go commands default to the local dev container
# (backend/internal/config). Override it or the PG* parts; see .env.example.

function Invoke-Step([string]$Name, [scriptblock]$Block) {
    Write-Host "==> $Name" -ForegroundColor Yellow
    & $Block
    if ($LASTEXITCODE -ne 0) { throw "$Name failed (exit $LASTEXITCODE)" }
}
