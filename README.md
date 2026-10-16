# BaiduPCS-GoPlus

在 [qjfoidnh/BaiduPCS-Go](https://github.com/qjfoidnh/BaiduPCS-Go) 基础上，增加 **Windows 图形界面 + 手动整理官方自动备份**。

**上传、更新文件和新建目录交给百度网盘官方自动备份，本工具只删除云端多余项、重命名已确认的正确版本。** 本地文件只读。

## 本版功能

- **先预览再整理**：`RENAME` 重命名、`REMOVE` 云端多余项、`KEEP` 一致、`WAIT` 等待官方备份、跳过需判断项。
- **按内容匹配版本**：云端 `报告(1).docx` 与本地 `报告.docx` 内容一致时，回收同名旧版，再重命名正确版本。不会按后缀数字或时间猜最新版；本地真实存在的括号文件名会保留。
- **逐项继续**：失败项跳过，其他独立项继续；某个重命名失败，只保留它关联的历史副本。
- **目录优先清理**：本地不存在的最外层云端目录直接整棵回收，不再扫描内部或逐文件删除；内部临时文件一并回收。预览明确标注内部数量/容量未知，须确认整目录范围；本身命中排除规则的目录仍保留。
- **只重试未完成项**：保持 GUI 启动窗口运行，失败后可直接重试剩余项，刷新页面也可恢复，无需重新全盘扫描。关闭服务后需要重新预览。
- **可恢复的删除**：进入网盘回收站，不永久删除、不清空回收站；清理超过文件数 10% 或 10 GB 时需额外确认。
- **进度与日志**：扫描、内容校验和整理时显示当前阶段、耗时、进度和最近日志。
- **目录浏览与哈希缓存**：本地、云端均支持浏览选择；可选快速校验以减少本地重复读取。

## Quick Start（Windows 64 位）

下载 [v0.4.1 或更新版本](https://github.com/wlaog/BaiduPCS-GoPlus/releases/latest)，无需安装 Go。

1. 解压程序到可写目录。首次使用，在该目录打开 PowerShell 导入自己的百度网盘 Cookies：

   ```powershell
   $env:BAIDUPCS_GO_CONFIG_DIR = Join-Path $PWD '.pcsconfig'
   .\BaiduPCS-GoPlus.exe login -cookies '在这里粘贴你的完整 Cookies'
   ```

   Cookies 获取方法见 [上游登录说明](docs/README_UPSTREAM.md#使用百度-cookies-来登录百度账号推荐)。凭据保存在 `.pcsconfig`，不要上传或分享该目录。

2. 双击 **`start-gui.cmd`**，选择对应的本地目录与云端备份子目录。
3. 等待官方备份完成，点击 **预览差异**。找不到本地最新内容的文件显示 `WAIT`，其相关历史版本会保留。
4. 核对后勾选确认并点击 **执行整理**。失败项可直接点击 **仅重试未完成项**；每次重试都会重新核对相关文件状态。

删除和重命名仍可能与官方任务竞争，建议等对应任务空闲或暂停后整理。程序会对 `31171` 忙碌错误退避重试，但无法给官方任务加锁。扫描不完整或本地根目录不可访问时拒绝清理。

整目录清理只复核目录身份及本地同路径仍不存在，不读取云端子树；预览后若官方客户端继续向其中写入，新增内容也会随目录进入回收站。

默认保持「重新核对完整内容」。取消后可使用哈希缓存，但依赖大小和修改时间未变，可能漏掉保留原大小和时间的内容修改。详见 [实现与验证](docs/PERFORMANCE_AND_PROGRESS.md)。

<details>
<summary>命令行与源码构建</summary>

```powershell
# 只读预览
.\BaiduPCS-GoPlus.exe reconcile plan --local 'D:/WorkSync' --remote '/备份/WorkSync' --hash

# 删除与重命名：会先显示预览并要求确认
.\BaiduPCS-GoPlus.exe reconcile apply --local 'D:/WorkSync' --remote '/备份/WorkSync'

# 源码构建（Go 1.23 或更高版本）
powershell -NoProfile -ExecutionPolicy Bypass -File tools/build.ps1 -Test
.\start-gui.cmd
```

执行记录位于 `.pcsconfig/reconcile-reports/`。CLI 每次重新扫描；不重新扫盘的剩余项重试由当前 GUI 服务保存。旧版完整镜像计划不可直接用于新版。

</details>

<details>
<summary>原作者文档与项目来源</summary>

- [原作者完整 README](docs/README_UPSTREAM.md)
- [开发所基于的 qjfoidnh/BaiduPCS-Go](https://github.com/qjfoidnh/BaiduPCS-Go)
- [本仓库 fork 来源 felixonmars/BaiduPCS-Go](https://github.com/felixonmars/BaiduPCS-Go)
- [最初的 iikira/BaiduPCS-Go](https://github.com/iikira/BaiduPCS-Go)

沿用 [Apache-2.0 许可证](LICENSE)。保留上游命令行能力，新整理流程见 `internal/reconcile/`。

</details>
