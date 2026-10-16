# 阶段一·可行性实测 —— 由使用者自己运行的引导脚本
#
# 作用: 用**独立的配置目录**登录并执行**纯只读**的 reconcile probe, 产出一份
#       可以直接发给他人审阅的报告(账号标识已隐去)。
#
# 安全说明:
#   * 本脚本只运行 `login` 与 `reconcile probe` 两条命令。
#   * `reconcile probe` 的实现写在只暴露「列举目录」和「读元信息」的接口上,
#     类型层面就无法删除/上传/移动 —— 它不会改变云端或本地任何内容。
#   * 使用独立配置目录(.pcsconfig), 不会影响你已有的 BaiduPCS-Go 登录状态。
#   * 本脚本不读取、不打印、不上传你的 BDUSS / Cookies / STOKEN。
#
# 用法:
#   powershell -NoProfile -ExecutionPolicy Bypass -File tools\run-verification.ps1
#
# 建议: 如果你愿意, 用一个**测试小号**运行, 而不是主账号。

param(
  # 你的本地工作目录(可选)。填了才会做「本地 vs 云端」的缺失项与哈希比对。
  [string]$LocalDir = '',
  # 已知的云端备份目录(可选)。不填则只做 --discover-roots, 帮你找出它的确切路径。
  [string]$RemoteRoot = '',
  # 哈希抽样数量。--hash-samples 会与本地同名文件逐一比对, 判定服务端 MD5 是否可用。
  [int]$HashSamples = 20,
  [switch]$SkipLogin
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$exe = Join-Path $repoRoot '.tools\BaiduPCS-GoPlus.exe'
$configDir = Join-Path $repoRoot '.pcsconfig'
$reportDir = Join-Path $repoRoot 'pcs-reconcile-reports'

if (-not (Test-Path $exe)) {
  throw "未找到 $exe。请先运行: powershell -NoProfile -ExecutionPolicy Bypass -File tools\build.ps1"
}

# 独立配置目录: 配置文件与上传断点库都写在这里, 不碰你的全局 BaiduPCS-Go 配置。
$env:BAIDUPCS_GO_CONFIG_DIR = $configDir
New-Item -ItemType Directory -Force -Path $configDir | Out-Null
New-Item -ItemType Directory -Force -Path $reportDir | Out-Null

Write-Host "======================================================================"
Write-Host " BaiduPCS-GoPlus · 只读可行性实测"
Write-Host "======================================================================"
Write-Host " 程序        : $exe"
Write-Host " 配置目录    : $configDir   (独立, 不影响你原有登录)"
Write-Host " 报告目录    : $reportDir"
Write-Host ""

if (-not $SkipLogin) {
  Write-Host "--- 步骤 1/2: 登录 ---"
  Write-Host " 需要一个百度网盘账号。推荐使用测试小号。"
  Write-Host " 若不想手动输入, 也可改用: .\$([System.IO.Path]::GetFileName($exe)) login -bduss=<BDUSS>"
  Write-Host ""
  & $exe login
  if ($LASTEXITCODE -ne 0) { throw "登录失败 (exit $LASTEXITCODE)" }
} else {
  Write-Host "--- 跳过登录 (--SkipLogin) ---"
}

Write-Host ""
Write-Host "--- 步骤 2/2: 只读探测 ---"
$args = @('reconcile', 'probe', '--discover-roots', '--redact', '--hash-samples', $HashSamples,
          '--json-out', $reportDir)
if ($RemoteRoot) { $args += @('--remote', $RemoteRoot) }
if ($LocalDir)   { $args += @('--local',  $LocalDir) }

Write-Host "执行: $([System.IO.Path]::GetFileName($exe)) $($args -join ' ')"
Write-Host ""
& $exe @args

Write-Host ""
Write-Host "======================================================================"
Write-Host " 完成。请把上面新生成的 JSON 报告文件发给我。"
Write-Host " 报告中账号名已隐去; 它只包含目录/文件名、大小、时间与哈希判定结果,"
Write-Host " 不包含 BDUSS / Cookies / STOKEN 等凭据。"
Write-Host "======================================================================"
Get-ChildItem -Path $reportDir -Filter 'probe-*.json' -ErrorAction SilentlyContinue |
  Sort-Object LastWriteTime -Descending |
  Select-Object -First 5 |
  ForEach-Object { Write-Host ("  最新报告: " + $_.FullName) }
