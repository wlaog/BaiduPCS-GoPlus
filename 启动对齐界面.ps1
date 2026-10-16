# ============================================================
#  对齐本地 · 图形界面启动器
#
#  作用:
#    1. 把配置目录指向项目内的 .pcsconfig (登录凭据在这里, 否则界面会显示未登录)
#    2. 打开 reconcile gui, 浏览器会自动弹出
#
#  用法 (任选一种):
#    右键本文件 -> 使用 PowerShell 运行
#    或在 PowerShell 里执行:  .\启动对齐界面.ps1
#
#  关闭方式: 在界面上点「关闭界面」, 或在此窗口按 Ctrl+C
#  本操作不影响网盘上的任何内容。
# ============================================================

param(
  [string]$LocalDir  = '',   # 预填本地目录, 例如 D:\WorkSync
  [string]$RemoteDir = '',   # 预填云端目录, 例如 /来自：本地电脑/WorkSync
  [switch]$NoBrowser         # 不自动打开浏览器, 只打印地址
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$exe  = Join-Path $root '.tools\BaiduPCS-GoPlus.exe'
if (Test-Path -LiteralPath (Join-Path $root 'BaiduPCS-GoPlus.exe')) {
  $exe = Join-Path $root 'BaiduPCS-GoPlus.exe'
}
$cfg  = Join-Path $root '.pcsconfig'

if (-not (Test-Path -LiteralPath $exe)) {
  Write-Host ''
  Write-Host '[错误] 找不到程序:' -ForegroundColor Red
  Write-Host "        $exe"
  Write-Host ''
  Write-Host '请先构建:'
  Write-Host '        powershell -NoProfile -ExecutionPolicy Bypass -File tools\build.ps1'
  Write-Host ''
  Read-Host '按回车键退出'
  exit 1
}

# 登录凭据在 .pcsconfig; 不设置这一项界面会显示"未登录"
$env:BAIDUPCS_GO_CONFIG_DIR = $cfg

Write-Host ''
Write-Host '============================================================' -ForegroundColor Cyan
Write-Host '  对齐本地 · 图形界面' -ForegroundColor Cyan
Write-Host '============================================================' -ForegroundColor Cyan
Write-Host "  程序    : $exe"
Write-Host "  配置目录: $cfg"
Write-Host ''
Write-Host '  浏览器会自动打开界面; 若没有, 请手动访问下面打印的地址。'
Write-Host '  关闭本窗口 (Ctrl+C) 即可结束界面。'
Write-Host '============================================================' -ForegroundColor Cyan
Write-Host ''

$guiArgs = @('reconcile', 'gui')
if ($LocalDir)  { $guiArgs += @('--local',  $LocalDir) }
if ($RemoteDir) { $guiArgs += @('--remote', $RemoteDir) }
if ($NoBrowser) { $guiArgs += '--no-browser' }

try {
  & $exe @guiArgs
} finally {
  Write-Host ''
  Write-Host '界面已退出。'
}
