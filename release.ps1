# release.ps1 - manual release from the Windows build box (fallback for, or
# double-check against, the GitHub Actions release workflow).
#
# Builds warpseed.exe, writes SHA256SUMS.txt, tags the commit, and publishes
# a GitHub Release with docs/release/release-notes-<version>.md as the body.
# Requires: wails CLI, gh CLI authenticated as the ZyraLabs account.
#
# Usage:  & .\release.ps1                # version read from wails.json
#         & .\release.ps1 -DryRun        # build + checksum only, no tag/release
param(
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $MyInvocation.MyCommand.Path
Push-Location $repo
try {
    $version = (Get-Content wails.json | ConvertFrom-Json).info.productVersion
    $tag = "v$version"
    Write-Host "== warpseed release $tag ==" -ForegroundColor Cyan

    if (git status --porcelain) { throw "working tree is not clean - commit first" }

    wails build -clean -platform windows/amd64 -o warpseed.exe
    if ($LASTEXITCODE -ne 0) { throw "wails build failed" }

    $exe = Join-Path $repo "build\bin\warpseed.exe"
    $sum = (Get-FileHash $exe -Algorithm SHA256).Hash.ToLower()
    $sums = Join-Path $repo "build\bin\SHA256SUMS.txt"
    "$sum  warpseed.exe" | Set-Content -Encoding ascii $sums
    Write-Host "SHA-256: $sum" -ForegroundColor Green

    if ($DryRun) { Write-Host "Dry run - not tagging or publishing."; return }

    $notes = Join-Path $repo "docs\release\release-notes-$version.md"
    if (-not (Test-Path $notes)) {
        $mm = $version -replace '\.\d+$', ''
        $notes = Join-Path $repo "docs\release\release-notes-$mm.md"
    }
    if (-not (Test-Path $notes)) { throw "no release notes at docs\release\release-notes-$version.md" }

    if (-not (git tag -l $tag)) {
        git tag -a $tag -m "warpseed $version"
        git push origin $tag
    }
    gh release create $tag $exe $sums --title "warpseed $tag" --notes-file $notes
    if ($LASTEXITCODE -ne 0) { throw "gh release create failed" }
    Write-Host "Published: https://github.com/ZyraLabs/warpseed/releases/tag/$tag" -ForegroundColor Green
}
finally { Pop-Location }
