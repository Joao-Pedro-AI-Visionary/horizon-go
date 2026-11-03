# Shared settings for the dev scripts. Dot-source it: . "$PSScriptRoot\_env.ps1"
$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
$env:Path = "$env:LOCALAPPDATA\Programs\go\bin;$env:USERPROFILE\go\bin;$env:Path"
$env:GOTOOLCHAIN = 'local'
if (-not $env:DATABASE_URL) {
    $env:DATABASE_URL = 'postgres://horizon:horizon@localhost:5450/horizon?sslmode=disable'
}

function Invoke-Step([string]$Name, [scriptblock]$Block) {
    Write-Host "==> $Name" -ForegroundColor Yellow
    & $Block
    if ($LASTEXITCODE -ne 0) { throw "$Name failed (exit $LASTEXITCODE)" }
}
