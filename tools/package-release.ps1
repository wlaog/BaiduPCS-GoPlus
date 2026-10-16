param(
  [ValidatePattern('^v\d+\.\d+\.\d+(-[a-zA-Z0-9.-]+)?$')]
  [string]$Version = 'v0.4.2'
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$outputDir = Join-Path $repoRoot '.tools/releases'
$stage = Join-Path $repoRoot ('.gotmp/release-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $stage, $outputDir | Out-Null

# Compile from source and run the affected tests. Never package a developer's
# existing binary, config, cookie files, reports, caches, or browser profile.
& (Join-Path $PSScriptRoot 'build.ps1') -SkipDeps -Test -Version $Version -OutName "release-$Version.exe"
Copy-Item -LiteralPath (Join-Path $repoRoot ".tools/release-$Version.exe") -Destination (Join-Path $stage 'BaiduPCS-GoPlus.exe')
foreach ($name in @('README.md', 'RELEASE_NOTES.md', 'LICENSE', 'NOTICE', 'start-gui.cmd')) {
  Copy-Item -LiteralPath (Join-Path $repoRoot $name) -Destination $stage
}
# Keep the Unicode filename without requiring a particular shell code page.
$psLauncher = Get-ChildItem -LiteralPath $repoRoot -Filter '*.ps1' -File |
  Where-Object { $_.Name -ne 'CODEBUDDY.ps1' }
if (@($psLauncher).Count -ne 1) { throw 'Expected one root PowerShell GUI launcher' }
Copy-Item -LiteralPath $psLauncher.FullName -Destination $stage
New-Item -ItemType Directory -Path (Join-Path $stage 'docs') | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot 'docs/README_UPSTREAM.md') -Destination (Join-Path $stage 'docs')
Copy-Item -LiteralPath (Join-Path $repoRoot 'docs/PERFORMANCE_AND_PROGRESS.md') -Destination (Join-Path $stage 'docs')

Add-Type -AssemblyName System.IO.Compression.FileSystem
$zipPath = Join-Path $outputDir "BaiduPCS-GoPlus-$Version-windows-amd64.zip"
if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath }
[IO.Compression.ZipFile]::CreateFromDirectory($stage, $zipPath)
$hash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
$checksums = Join-Path $outputDir 'SHA256SUMS.txt'
[IO.File]::WriteAllText($checksums, "$hash  $([IO.Path]::GetFileName($zipPath))`n", [Text.Encoding]::ASCII)
Write-Host "Release package: $zipPath"
Write-Host "SHA256: $hash"
