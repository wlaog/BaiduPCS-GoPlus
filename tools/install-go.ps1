$ErrorActionPreference = 'Stop'
$tools   = 'D:\projects\BaiduPCS-GoPlus\.tools'
$zip     = Join-Path $tools 'go.zip'
$dest    = Join-Path $tools 'go'
New-Item -ItemType Directory -Path $tools -Force | Out-Null

$ver = '1.23.12'
$url = "https://go.dev/dl/go$ver.windows-amd64.zip"
Write-Output "Downloading $url"
$sw = [System.Diagnostics.Stopwatch]::StartNew()
$wc = New-Object System.Net.WebClient
$wc.DownloadFile($url, $zip)
$sw.Stop()
Write-Output ("Downloaded {0:N1} MB in {1:N0}s" -f ((Get-Item $zip).Length / 1MB), $sw.Elapsed.TotalSeconds)

Write-Output "Extracting to $dest"
if (Test-Path $dest) { Remove-Item -Recurse -Force $dest }
Expand-Archive -LiteralPath $zip -DestinationPath $tools -Force
Remove-Item -LiteralPath $zip -Force

$goExe = Join-Path $dest 'bin\go.exe'
Write-Output "go exe: $goExe exists=$(Test-Path $goExe)"
& $goExe version
& $goExe env GOROOT
Write-Output "DONE"
