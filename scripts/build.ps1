$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
& go build -trimpath -ldflags "-s -w" -o bin\herdr-git-upstream.exe .\cmd\herdr-git-upstream
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
