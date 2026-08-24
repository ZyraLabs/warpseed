# update.ps1 — pull the latest warpseed and build it (run from anywhere).
# The build machine is not necessarily the machine that runs the app, so
# this only builds; pass -Run when you do want it launched afterwards.
#
# Usage:  & C:\path\to\warpseed\update.ps1        # update + build
#         & C:\path\to\warpseed\update.ps1 -Run   # ... and launch it
param(
    [switch]$Run
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

    if ($Run) {
        Start-Process $exe
    }
}
finally {
    Pop-Location
}
