# BaiduPCS-GoPlus

在 [qjfoidnh/BaiduPCS-Go](https://github.com/qjfoidnh/BaiduPCS-Go) 基础上，增加 **Windows 图形界面 + 手动对齐本地文件夹**。

平时继续用百度网盘官方客户端自动备份；需要整理时，以本地文件夹为准，手动上传缺失文件、覆盖内容不同的文件，并按确认结果清理云端多余项。只处理指定目录，不修改本地文件。

## 本版增加了什么

- **先预览再执行**：查看待上传、待覆盖、云端多余及冲突项。
- **目录浏览选择**：本地与云端都可以逐级选择文件夹，也支持直接输入路径。
- **可恢复的清理**：默认移入云端隔离区，也可选择网盘回收站；不做永久删除。
- **大批量清理确认**：清理超过所选云端目录文件数的 10%，或容量超过 10 GB 时，需要额外确认。
- **简洁的本地界面**：使用说明、探测和日志默认折叠；Windows 使用独立 Edge 应用窗口。
- 保留上游命令行能力，新增 `reconcile probe / plan / apply / gui`。

## Quick Start（Windows 64 位）

1. 从 [Releases](https://github.com/wlaog/BaiduPCS-GoPlus/releases/latest) 下载 `BaiduPCS-GoPlus-v版本号-windows-amd64.zip`，**完整解压**到可写目录，无需安装 Go。
2. 首次使用先登录。打开解压目录中的 PowerShell，按下方命令导入你自己的百度网盘 Cookies：

   ```powershell
   $env:BAIDUPCS_GO_CONFIG_DIR = Join-Path $PWD '.pcsconfig'
   .\BaiduPCS-GoPlus.exe login -cookies '在这里粘贴你的完整 Cookies'
   ```

   Cookies 获取方法见 [上游登录说明](docs/README_UPSTREAM.md#使用百度-cookies-来登录百度账号推荐)。本程序与官方客户端使用独立登录状态；账号凭据保存在本目录 `.pcsconfig`，不要上传或分享该目录。

3. 双击 **`start-gui.cmd`**，选择相互对应的本地文件夹与云端备份子目录，点击 **预览差异**。已登录的用户直接从这一步开始。
4. 核对预览后执行。默认只上传和覆盖；如需清理，勾选“同时清理预览中列出的云端多余文件”。完成后再次预览确认结果。

> 首次请使用独立测试目录验证路径和备份行为。执行整理前暂停对应的官方备份任务，完成后再恢复；不能保证与正在上传的官方任务无冲突。覆盖会替换云端原内容，重要文件请先保留独立备份。

隔离区默认位于 `/BackupReconcile_Quarantine/<时间戳>/`，恢复时按执行报告中的路径将文件移回。回收站的恢复期限以百度网盘实际显示为准。

升级时退出旧程序，解压新版并保留自己的 `.pcsconfig`。本项目不附带任何账号或登录凭据。

<details>
<summary>命令行与源码构建</summary>

```powershell
# 以下命令只读，不修改云端
.\BaiduPCS-GoPlus.exe reconcile probe --discover-roots
.\BaiduPCS-GoPlus.exe reconcile plan --local 'D:/WorkSync' --remote '/备份/WorkSync' --hash

# 源码构建：安装 Go 1.23 或更高版本后，在仓库根目录执行
powershell -NoProfile -ExecutionPolicy Bypass -File tools/build.ps1 -Test
.\start-gui.cmd
```

`reconcile plan` 是 dry-run；`reconcile apply` 会修改云端并要求确认，参数见 `reconcile apply --help`。执行报告位于 `.pcsconfig/reconcile-reports/`。

</details>

<details>
<summary>原作者文档与项目来源</summary>

- [原作者完整 README（保留副本）](docs/README_UPSTREAM.md)
- [开发所基于的 qjfoidnh/BaiduPCS-Go](https://github.com/qjfoidnh/BaiduPCS-Go)
- [本仓库 fork 来源 felixonmars/BaiduPCS-Go](https://github.com/felixonmars/BaiduPCS-Go)
- [最初的 iikira/BaiduPCS-Go](https://github.com/iikira/BaiduPCS-Go)

感谢上游作者。沿用 [Apache-2.0 许可证](LICENSE)，本版修改见 [NOTICE](NOTICE)。

</details>
