# update.ps1 — pull the latest warpseed and rebuild (run from anywhere).
# Usage:  & C:\Users\Dave\warpseed\update.ps1            # update, build, launch
#         & C:\Users\Dave\warpseed\update.ps1 -NoLaunch  # update + build only
param(
    [switch]$NoLaunch
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $MyInvocation.MyCommand.Path

Push-Location $repo
try {
    Write-Host "== warpseed update ==" -ForegroundColor Cyan

    # Discard build-regenerated files (wailsjs bindings, go.mod churn) —
    # this checkout never has hand edits; the Spark is the only committer.
    git checkout -- .
    git pull --ff-only
    if ($LASTEXITCODE -ne 0) { throw "git pull failed" }

    Write-Host ("Now at: " + (git log --oneline -1)) -ForegroundColor Cyan

    wails build
    if ($LASTEXITCODE -ne 0) { throw "wails build failed" }

    $exe = Join-Path $repo "build\bin\warpseed.exe"
    Write-Host "Built: $exe" -ForegroundColor Green

    if (-not $NoLaunch) {
        Start-Process $exe
    }
}
finally {
    Pop-Location
}
