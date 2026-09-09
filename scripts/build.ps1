$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
& go build -trimpath -ldflags "-s -w" -o bin\herdr-pull-status.exe .\cmd\herdr-pull-status
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
