# BaiduPCS-GoPlus build helper (Windows PowerShell / PowerShell 7)
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\build.ps1
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\build.ps1 -Test
#
# This script does not modify system state: it only sets environment variables
# for the current process and writes the binary into .\.tools\.
#
# A portable Go toolchain is expected at .\.tools\go (see tools\install-go.ps1).
# If it is missing, an already-installed `go` on PATH is used instead.

param(
  [switch]$Test,
  [switch]$SkipDeps,
  [string]$OutName = 'BaiduPCS-GoPlus.exe',
  [string]$Version = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$toolsDir = Join-Path $repoRoot '.tools'
$goRoot   = Join-Path $toolsDir 'go'
$goExe    = Join-Path $goRoot 'bin\go.exe'

if (-not (Test-Path $goExe)) {
  $onPath = Get-Command go -ErrorAction SilentlyContinue
  if ($onPath) {
    $goExe = $onPath.Source
    Write-Host "Using Go from PATH: $goExe"
  } else {
    throw "Go toolchain not found. Run tools\install-go.ps1 first, or install Go >= 1.23."
  }
} else {
  $env:GOROOT = $goRoot
  $env:PATH = "$goRoot\bin;$env:PATH"
}

# Keep every cache inside the repository so the build never writes outside it
# and never depends on the user's global Go environment.
$env:GOPATH   = Join-Path $repoRoot '.gopath'
$env:GOCACHE  = Join-Path $repoRoot '.gocache'
$env:GOTMPDIR = Join-Path $repoRoot '.gotmp'
$env:GOFLAGS  = '-mod=mod'
New-Item -ItemType Directory -Force -Path $env:GOTMPDIR | Out-Null
$env:TEMP = $env:GOTMPDIR
$env:TMP = $env:GOTMPDIR

Push-Location $repoRoot
try {
  Write-Host "Go: $(& $goExe version)"

  if (-not $SkipDeps) {
    Write-Host "Downloading module dependencies..."
    & $goExe mod download
    if ($LASTEXITCODE -ne 0) { throw "go mod download failed with exit code $LASTEXITCODE" }
  }

  $out = Join-Path $toolsDir $OutName
  Write-Host "Building $out ..."
  $linkFlags = '-s -w'
  if ($Version) { $linkFlags += " -X main.Version=$Version" }
  & $goExe build -trimpath -ldflags $linkFlags -o $out .
  if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
  Write-Host ("Built {0} ({1:N1} MB)" -f $out, ((Get-Item $out).Length / 1MB))

  if ($Test) {
    Write-Host "Running reconcile, GUI and upload regression tests..."
    & $goExe test -count=1 ./internal/reconcile/... ./internal/pcscommand ./internal/pcsfunctions/pcsupload ./baidupcs
    if ($LASTEXITCODE -ne 0) { throw "tests failed with exit code $LASTEXITCODE" }
  }

  Write-Host ""
  Write-Host "Next steps (read-only, safe against a real account):"
  Write-Host "  `$env:BAIDUPCS_GO_CONFIG_DIR = '<a dedicated config dir>'"
  Write-Host "  .\.tools\$OutName login --help"
  Write-Host "  .\.tools\$OutName reconcile probe --discover-roots"
  Write-Host "  .\start-gui.cmd"
}
finally {
  Pop-Location
}
