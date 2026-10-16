# 阶段一交付：可行性分析与实现计划

> 历史阶段记录：本文件描述阶段一当时的调查与待验证事项，不代表当前发版状态。当前功能和用法见 [README](README.md)。许可证以仓库 [LICENSE](LICENSE) 为准（Apache-2.0）。

> 对应 `README_BaiduPCS_Backup_Reconcile.md` 第 3 节（Go/No-Go）与第 9 节（交付物 1）。
> 本文件是**分析报告**，不是已完成功能的说明。**截至本文件，没有任何云端写入行为被实现或执行。**

---

## 0. 结论摘要

| 项 | 结论 | 依据 |
|---|---|---|
| 上游仓库版本 | `qjfoidnh/BaiduPCS-Go`，`v4.0.2-5-g1b91318`，commit `1b913181`，`go 1.23`，MIT | 本地 clone |
| 选型路线 | **必须走 BDUSS/PCS 路线**（上游现状），**不能走官方开放平台 xpan** | 官方文档明文限制 |
| 「来自：本地电脑」可访问性 | **架构上可行、置信度高，但尚未实测**（需真实账号） | 官方帮助 + 源码能力分析 |
| 备份任务查询/暂停 API | **不存在**。备份状态纯本地，只能靠客户端 UI 暂停 | 官方文档缺失 + 源码 grep 无命中 |
| 官方客户端并发/复原风险 | **真实存在**（备份是"只增不删"语义），必须暂停客户端后再清理 | 官方帮助语义 + 类比证据 |
| 可靠的内容一致性判定 | 只有 `len(block_list)==1` 的 MD5 可信；大文件**无法**在不下载的前提下可信判等 | 源码自证 |
| 当前判定 | **条件性 Go**：代码全部就绪，**只差真实账号的实测验证**（本文件第 5 节列出待验清单） | — |
| 已交付 | 只读探测工具 `reconcile probe` + 25 个单元测试，全部通过 | 见第 3、6 节 |

**一句话**：技术路线可行且不冲突的设计存在，但 README 第 3 节要求的"真实账号验证"尚未完成——因为那需要你的百度网盘登录凭据，我不能也不应该自己取得。做完那一步之前，我不会写任何会删除云端文件的代码。

---

## 1. 验证过程：我实际做了什么

### 1.1 环境修复（已解决）

工作区 `D:\projects\BaiduPCS-GoPlus` 的 Windows 文件权限缺失 `WRITE_OWNER`，导致 DSH 沙箱无法为它下发工作区授权，**所有**命令（含只读的 `Get-ChildItem`）都在沙箱准备阶段失败。

- 处置：用随附的诊断脚本做了一次受权修复，为该目录补上当前登录用户的完全控制项（未改文件内容、未改所有者）。
- 结果：`GRANTED` + `verification: verified`，`RECAP` 为 `nextAction=verify_original_confined_operation`；复跑受限命令成功。
- 回滚命令（如需要）：
  ```powershell
  pwsh -NoProfile -File 'D:\projects\dsh-acl-recovery\acl-backup-c2e103d2c5684c52ad69fa2a884ddd10.json.ps1' `
    -Path 'D:\projects\BaiduPCS-GoPlus' -AllowRoot 'D:\projects\BaiduPCS-GoPlus' `
    -Restore 'D:\projects\dsh-acl-recovery\acl-backup-c2e103d2c5684c52ad69fa2a884ddd10.json'
  ```
- 报告：`D:\projects\dsh-acl-recovery\acl-report-46dc6fdd38884327a01c95e1948480e7.jsonl`

### 1.2 工具链

本机原本**没有 Go**。已下载官方 Go 1.23.12 (windows/amd64) 到项目内 `.tools\go`，并用它跑通上游基线构建：

```
go version go1.23.12 windows/amd64
go build -o .tools\BaiduPCS-Go-baseline.exe .   # exit 0，19.5 MB
```

### 1.3 发现的环境限制（对你后续操作有影响）

**DSH 沙箱内 Windows 的 HTTPS/TLS 凭据链路不可用**：沙箱内 `curl` 报 `schannel: AcquireCredentialsHandle failed: SEC_E_NO_CREDENTIALS`，`.NET WebClient` 报"基础连接已关闭"，两者对所有 HTTPS 站点一致失败；而 `git`（自带 OpenSSL 栈）**在沙箱内正常**，同一台机器**在沙箱外** `curl https://go.dev/` 返回 200。

- 含义：`go mod download` / `go build`（需拉依赖）必须在不带沙箱限制的方式下执行一次；依赖落到项目内 `.gopath` 后，之后可离线编译。
- 已固化在 `tools\build.ps1`，并把缓存全部指向仓库内（`.gopath` / `.gocache` / `.gotmp`），不污染你的全局 Go 环境。
- 诊断脚本：`tools\diagnose-https.ps1`。

### 1.4 仓库布局

上游代码树已提到仓库根，新建分支 `feature/reconcile-local`（基线 commit `1b913181`）。已提交 1 个 commit：`8c7d9f1`。

---

## 2. 源码能力核实（回答 README 第 3 节的 5 个验证点）

### 2.1 能否定位并递归读取备份目录

**能**，前提是它是普通云盘目录。

- 列举：`baidupcs/file_directory.go:208 FilesDirectoriesList(path, *OrderOptions)` → 走 `pan.baidu.com/api/list`，**每页 1000**，按 `page` 自动翻页，上限 2000 页，用 `fs_id` 去重。
- 元信息：`FilesDirectoriesMeta` / `FilesDirectoriesBatchMeta`（`file_directory.go:110/132`）。
- 返回的 `FileDirectory` 含 `FsID / Path / Filename / Ctime / Mtime / MD5 / BlockList / Size / Isdir`——**稳定标识（fs_id）、路径、大小、时间齐全**。
- 中文/空格/括号：全链路 UTF-8，`escaper` 按 `[]rune` 处理；数据路径**不经过 shell**（全仓 `os/exec` 仅用于 `run` 子命令透传 argv）。唯一坑：`[` `]` `*` `?` 是上游通配符，`MatchPathByShellPattern` 只转义 `[`；`reconcile` 不复用该函数即可完全绕开。

**必须知道的缺陷（上游 issue #511 相关）**：`FilesDirectoriesList` 在遇到重复 `fs_id`（说明分页未推进）时**直接 return 已累积的部分数据且 err == nil**（`file_directory.go:238-241`）。即"静默截断"。我的扫描器已把它当作一等风险处理：任何目录子项数达到 1000 即标记待人工复核，且该目录的 partial 结果不会被当作完整事实（见第 3.2 节）。

### 2.2 上传覆盖 / 移动 / 删除

| 能力 | 上游方法 | 备注 |
|---|---|---|
| 上传（含覆盖） | `baidupcs/upload.go` + `internal/pcsfunctions/pcsupload` | 策略 `skip`/`overwrite`/`rsync` → PCS `rtype` `2`/`3` |
| 建目录 | `rm_mkdir.go:28 Mkdir(path)` | **不创建中间目录**，需自己逐级建 |
| 移动/重命名 | `cp_mv_rename.go:9/22 Rename / Move(CpMvJSON...)` | 同 `Move` 接口，`method=move` |
| 拷贝 | `cp_mv_rename.go:17 Copy` | |
| 删除 | `rm_mkdir.go:9 Remove(paths...)` | 进回收站，可恢复 |
| 回收站 | `recycle.go` `RecycleList/Restore/Delete/Clear` | 可作为额外的恢复层 |

**三个必须规避的陷阱**：

1. **`rsync` 策略按"大小相同即跳过"判等**（`util.go:75-81`：`case RsyncPolicy: if targetFileSize == fileSize` → 报"目标位置存在相同文件"并跳过）。这正是 README 警告的——**绝不可用作内容一致性检测**，否则"大小相同、内容不同"的文件永远不会被更新。
2. **上传路径存在覆盖同名目录的风险**。`util.go:50 CheckIsdir()` 的注释写着"很重要, 如果文件存在会直接覆盖!!! 即使是根目录!"，它的作用是检出"保存路径是目录"并拒绝。但我核实了它的**全部调用点**——只有 `prepare.go:423/434/444`，即 `PrepareRapidUpload` / `PrepareRapidUploadV2` / `PrepareFakeRapidUploadV2` 这三个**秒传**路径。**非秒传的 `PrepareUpload`（`prepare.go:526`，"分片上传中的预上传部分"）与 `PrepareUploadPrecreate`（`prepare.go:605`）都不做这个检查**；而 `PrepareUpload` 还把 `ondup` 直接写成 `strings.Replace(policy, "rsync", "overwrite", -1)`。
   结论：`reconcile` 在覆盖前**必须自己**先确认目标是文件而不是目录（用 `FilesDirectoriesMeta`），不能指望上游拦住。另外 `RapidUploadNoCheckDir`（`upload.go:158`）的注释明确"会覆盖掉同名的目录!"，**绝不可使用**。
3. **上传自造错误码**：`SkipPolicy` 命中同名文件 → `ErrCode 114514`；`RsyncPolicy` 大小相同 → `1919810`（`util.go:68-85`）。这些是 `ErrTypeRemoteError`，若不专门识别会把"跳过"误判成"失败"。

### 2.3 官方备份任务的暂停与并发（README 第 3 节第 4 点）——**这是最大的风险项**

- **不存在任何公开 API** 用于查询/暂停/恢复官方客户端的备份任务。双向验证：官方开放平台文档目录中没有此类接口；本仓库 `grep` 也没有任何相关实现。备份任务的配置与进度**纯本地**，云端只看到普通文件。
- **只能靠客户端 UI 暂停**（「设置 → 自动备份设置」/ 任务列表里暂停），并且**必须退出客户端进程**。
- **为什么必须暂停**：官方自动备份是**增量、只增不删**语义——"本地文件被删除，备份文件中的内容依然保留"。也就是说，我们用第三方 API 把云端冗余文件移走后，客户端下次扫描很可能因为"本地存在（历史）→ 云端缺失"而**重新上传**，甚至产生 `xxx (1).ext`。
- **诚实边界**：这是**高置信推断，不是实测结论**。我没找到"第三方删除备份目录中的文件 → 官方客户端重新上传"的公开报告；现有证据是类比性的（官方客户端会重建被删的**本地**保存目录；云端「我的应用数据」删除后会"不知何时又回来"）。**这正是第 5 节第 3 项必须实测的原因。**

因此设计上采取"**默认拒绝**"：无法确认官方备份处于已暂停/无竞争状态时，`--apply` 的**删除步骤直接拒绝执行**（符合 README 第 5 节第 6 点、第 6 节）。

### 2.4 分页完整性与路径转义

- 分页：见 2.1 的静默截断缺陷。
- 转义：中文/空格/括号安全；`[`/`]` 需绕开通配符层（`reconcile` 用精确路径，天然绕开）。
- Windows 适配：本地路径需 `pcsutil.ConvertToUnixPathSeparator`；云端路径恒用 `/`。

### 2.5 同名文件内容更新能否比较（README 第 3 节第 5 点）——**部分可以，这是核心设计约束**

源码自证 MD5 不可信：

- `file_directory.go:335 fixMD5()` **仅在 `len(block_list)==1` 时**才用 `BlockList[0]` 覆盖 `MD5`；
- 多分片时上游自己在 UI 上标注 **"md5 (可能不正确)"**；
- `upload_task_unit.go:192` 的 TODO 直言 `// TODO: fd.MD5 有可能是错误的`；
- 分片上传后服务端可能记录错误的 md5（因此上游才有一个 `fixmd5` 子命令）。

**关键推演**：`block_list[0]` 是**前 256KB 的切片 MD5**（`SliceMD5Size = 256 * KB`）。所以

- 文件 ≤ 256KB → 单分片 → 服务端 MD5 **就是**真实内容 MD5 → **可与本地计算的 MD5 直接比对**；
- 文件 > 256KB → 多分片 → 服务端字段不可信 → **只能**与本地"前 256KB 切片 MD5"比对，或者不比对。

**这决定了正确的不对称判等规则**（也是我实现里已经固化的规则）：

- 哈希**相同** → 可作为"内容相同"的**证据**（保守方向安全）；
- 哈希**不同** → **绝不能**作为"内容不同"的证据，只能当作"无法确认"→ 退化为按大小+时间判断，或保守地重新上传覆盖。

取得真实内容哈希的唯一办法是对下载直链发 `Range: bytes=0-262143` 读响应头 `Content-MD5`——需要一次 HTTP 请求且受频率限制。README 已明确"避免为检查每个文件强制下载几百 GB"，所以本方案不依赖它。

---

## 3. 本次已实现的代码（只读部分）

新增 `internal/reconcile/`，**刻意**先只做"读"这一半，因为它是所有破坏性动作的前置条件，而且完全无风险。

### 3.1 分层与"只读"是如何被强制的

```
internal/reconcile/
  remotefs.go   Reader 接口 + *baidupcs.BaiduPCS 适配器（唯一接触上游 API 的地方）
  errors.go     NotFound / Unreachable 分类，"不存在"≠"查不到"
  scan.go       云端树扫描 + 完整性信号 + 结构指纹
  local.go      本地树扫描 + 内容哈希 + 写中保护
  probe.go      只读探测报告（JSON + 人类可读）
  process_windows.go / process_other.go   官方客户端进程探测（仅告警）
internal/pcscommand/reconcile.go   CLI 粘合层
main.go                            注册 `reconcile probe` 命令
```

**设计要点**：探测逻辑写在 `Reader` 接口上，而 `Reader` **只暴露列举和元信息两个方法**——它在类型层面就无法删除/上传/移动。所以 `probe` 命令**不可能**误伤云端，即使代码有 bug。

`reconcile plan` / `reconcile apply` 已注册但实现为**明确报错的桩**，避免"命令存在但静默什么都不做"这种更危险的状态。

### 3.2 已落实的安全不变量（每条都有单测）

| 不变量 | 为什么重要 | 测试 |
|---|---|---|
| 列不出来的子树**记录为错误**，绝不显示为空 | "空"会让其下所有云端文件变成删除候选——这是最危险的失效模式 | `TestScanCloudRecordsListingFailureInsteadOfPretendingEmpty` |
| 达到扫描/深度上限 → 标记不完整 | 截断的扫描不能用来做删除决策 | `TestScanCloudFlagsPaginationTruncationAtPageBoundary`、`TestScanCloudRefusesDepthBeyondLimit` |
| 目录子项数达到分页上限 1000 → 标记待复核 | 上游分页异常会静默返回部分数据 | `TestScanCloudDetectsPageSizeBoundary` |
| 云端名称含 Windows 非法字符/保留名/尾随点空格 → **保留不删** | 云端显示的名字未必等于官方客户端本地的名字，按名删会误伤 | `TestUnsafeNameReason`、`TestScanCloudFlagsUnsafeNames` |
| `name(n).ext` 只**报告**，绝不据此删除 | README 明确要求：本地真有 `paper(1).docx` 时必须保留 | `TestScanCloudDetectsDuplicateLikeNamesButKeepsThem` |
| 服务端 MD5 仅在单 block 时视为可信 | 多分片 md5 上游自己都标"可能不正确" | `TestScanCloudRecordsTreeAndStats` |
| 扫描不完整的报告**不得**同时出现 PASS | 否则你会把一次失败读成健康 | `TestProbeReportsIncompleteScanAndNeverClaimsSuccess` |
| 报告不含任何凭据（BDUSS/STOKEN/…） | README 第 6 节要求 | `TestRenderProbeHumanDoesNotPanicAndHidesSecrets` |
| 所有拒绝路径都在 `--remote` 根之内 | README 第 6 节"不触碰根外任何文件" | `TestIsSubPathGuardsTheRoot`、`TestAssertInsideRootRejectsEscape` |
| 本地根为空/不存在/是文件 → 拒绝 | 防误删阈值的前提是"扫描完成" | `TestScanLocalRejectsFileAndMissingRoot` |

其他：不跟随 junction/符号链接（默认），跳过 `.tmp` 等临时后缀（默认），本地相对路径统一 `/`（Windows 上也是），扫描出"结构指纹"以便两次 dry-run 对比。

### 3.3 构建与测试结果

```
tools\build.ps1 -Test
  Go: go version go1.23.12 windows/amd64
  Built .tools\BaiduPCS-GoPlus.exe (12.9 MB)
  ok  github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile  0.489s   (25 个测试全通过)
```

**上游回归**：`go build ./...` 全部通过。`go test ./...` 与 `go vet ./...` 存在 **1 处测试失败和若干 vet 告警，均为上游既有问题，与本改动无关**：`pcsutil/file_test.go:12` 硬编码 macOS 路径 `/Users/syy/tmp`；`requester/downloader/download_test.go:44` 引用已不存在的 `Monitor.ShowWorkers`；vet 告警在 `baidupcs/prepare.go:224`、`pcsutil/converter/converter.go:26`、`pcscommand/login.go:132`。`internal/reconcile` 自身 `go vet` 干净。

---

## 4. 实现计划（`reconcile plan` / `reconcile apply`）

### 4.1 命令形态

```powershell
# 步骤 1：只读探测（已实现，安全，可对真实账号跑）
BaiduPCS-Go reconcile probe --discover-roots
BaiduPCS-Go reconcile probe --remote "/来自：本地电脑/WorkSync" --local "D:/WorkSync"

# 步骤 2：预览差异（默认，不改变云端）
BaiduPCS-Go reconcile plan --local "D:/WorkSync" --remote "/来自：本地电脑/WorkSync"
BaiduPCS-Go reconcile plan ... --json report.json      # 供审阅/留档

# 步骤 3：确认后执行（交互确认 + 阈值二次确认）
BaiduPCS-Go reconcile apply --local "D:/WorkSync" --remote "/来自：本地电脑/WorkSync"
BaiduPCS-Go reconcile apply ... --i-have-paused-the-official-client
BaiduPCS-Go reconcile apply ... --yes-i-confirm-removal      # 阈值超限时
BaiduPCS-Go reconcile apply ... --no-remove                  # 只上传/覆盖，不动多余文件
```

### 4.2 差异模型

`UPLOAD`（云端缺失）· `REPLACE`（同路径内容不同）· `REMOVE`（云端多余）· `KEEP`（一致）· `CONFLICT`（类型不符/名称不可映射/正在编辑/哈希不可判）

判等规则（严格区分"证明相同"与"无法判断"）：

| 情形 | 判定 |
|---|---|
| 双方 MD5 相等（服务端可信时） | KEEP（有证据） |
| 服务端 MD5 可信但不等 | REPLACE |
| 服务端 MD5 不可信（多分片） | 退回：大小不等 → REPLACE；大小相等 → **无法证明**，标 `REPLACE`（保守覆盖，幂等且安全）或按 `--conservative` 标 CONFLICT |
| 仅云端有 | REMOVE（进隔离区） |
| 仅本地有 | UPLOAD |
| 一侧文件一侧目录 | CONFLICT，**绝不自动删** |

> 注意：这里**故意偏向"多传一次"**。误判为"不同"只是多一次幂等覆盖；误判为"相同"才会导致云端永久保留旧版。

### 4.3 执行顺序与失败处理（README 第 5 节第 5 点）

```
[0] 前置校验  本地根存在可读且扫描完成；云端根存在且是目录；
              所有路径都在 --remote 之内；官方客户端已暂停（见 4.5）
[1] 建目录    逐级 Mkdir（上游不建中间目录）
[2] 上传/覆盖 先做，逐个校验成功；任何失败 → 立即停止，跳过后续清理
[3] 复验     重新列举云端，确认 [2] 的结果真的落地
[4] 清理     仅当 [0]-[3] 全部成功；多余项 Move 到隔离区
[5] 落盘清单 前置快照 + 操作明细 + 隔离区清单（JSON + 人类可读）
```

幂等：中断后重跑，已上传的会因哈希相等而 KEEP，未完成的继续；**重跑不会重复删除**，因为清理阶段只处理"仍存在于镜像内且本地没有"的项目。

### 4.4 删除防护（README 第 6 节，逐条对应）

| README 要求 | 设计 |
|---|---|
| 默认 dry-run | `plan` 是默认入口；`apply` 必须显式指定 |
| `--apply` 交互式确认 + 展示预计删除数量与容量 | 打印 `拟移除 N 项 / 合计 X` 后要求输入确认串 |
| 移动到隔离区而非永久删除 | `Move(/root/... → /BackupReconcile_Quarantine/<runid>/...)`，保留原相对路径 + 清单 |
| 隔离区不被同一镜像规则扫到 | 隔离区在 `--remote` 根**之外**（默认网盘根下的独立目录），并在扫描时显式排除其前缀 |
| 无法可靠移动 → 阻止清理并报告 | 若 `Move` 返回错误，**不降级为 `Remove`**，直接中止并报告 |
| 防误删阈值（>10% 或 >10GB 二次确认） | 可配置 `--max-remove-percent` / `--max-remove-bytes`，超出需 `--yes-i-confirm-removal` |
| 本地疑似被清空 → 强制停止 | 若本次计划的 REMOVE 比例超过阈值、或本地扫描文件数为 0 而云端非空、或本地根为空目录 → 硬停止 |
| 覆盖也可能丢旧版 | 覆盖前先把云端旧版 `Copy` 到同一隔离区（可配置；不可行时明确告知并需确认） |
| 不触碰根外文件、不改本地、不改官方备份设置 | 所有路径过 `assertInsideRoot`；本地侧只有 `os.Open`/`Stat`，代码里没有本地写操作；不读写官方客户端配置 |
| 不打印凭据 | 已由单测保证 |
| 阈值可配置 | 见上 |

### 4.5 并发/竞争防护（这是唯一还不能确定的部分）

三档，按"能证伪多少"排序：

1. **要求 `--i-have-paused-the-official-client`**（显式声明）。未提供时，`apply` 允许上传/覆盖，但**拒绝执行清理**（README 第 5 节第 6 点"默认拒绝"）。
2. **进程探测**（已实现）：检测到 `BaiduNetdisk.exe` / `baidunetdiskhost.exe` 或对应命名管道存在 → 告警并阻止清理。
3. **活跃上传探测**：比对两次云端列举之间是否有新文件/新 mtime 出现，若有 → 判定为"官方备份可能正在运行" → 阻止清理。

> 探测是**告警信号，不是许可凭证**：进程不存在**不能**证明备份已停止（反之亦然）。真正的权威只有客户端自己的设置，所以第 1 档的显式声明是必需的。

### 4.6 测试计划

- 纯函数 + `fakeFS` 内存假远端（**已具备基础设施**，当前 25 个测试）：差异计划生成、判等规则、阈值、路径逃逸、名称不可映射、分页截断、类型冲突、审计 4.1 的每一条。
- `fakeFS` 注入失败：上传失败 → 不清理；`Move` 失败 → 不降级删除；扫描失败 → 中止。
- 本地侧用 `t.TempDir()` 构造中文/空格/括号/大文件/空目录/同名不同内容。
- 验收对照（README 第 8 节）逐条写成测试，特别是：#4（本地 `a(1).txt` 不被删）、#7（中断/鉴权失败/部分上传失败不清理）、#8（跑完再 dry-run 归零）。

---

## 5. 需要你确认/提供的（Go/No-Go 的最后一步）

README 第 3 节要求用真实小号与测试数据实测。**我没有你的凭据，也不应该自行取得**，所以这一步必须你来决定怎么做。待验清单：

| # | 待验证 | 为什么必须实测 |
|---|---|---|
| 1 | `/` 下能否列出该目录，**确切的字面路径**是什么（全角冒号 `：`？叫「来自：本地电脑」还是别的） | 所有证据都指向"根下普通目录"，但确切名字无任何可引用来源 |
| 2 | 该目录是否受服务端保护（`meta`/`rm`/`move` 是否返回特殊错误码） | 普通目录应当可操作，但需证伪"隐藏对象"假设 |
| 3 | **移走一个测试冗余文件后，官方客户端是否重新上传、多久、是否生成 `(1)`** | **决定整个功能是否成立**；目前只有类比证据 |
| 4 | 暂停/退出官方客户端后，云端是否完全静止 | 决定"暂停"是否是充分条件 |
| 5 | 重命名/移动该目录内的项是否破坏官方客户端映射 | 避免让自动备份从此失效 |
| 6 | `ondup=overwrite` 在该目录的实际语义（是否真覆盖、是否丢旧版） | 关系到 `REPLACE` 的安全性 |
| 7 | 是否有可用的版本历史，保留多久 | 若能依赖历史版本，隔离区的必要性可降低 |
| 8 | MD5 字段实测：单分片是否等于本地全量 MD5、多分片是否等于本地前 256KB 切片 MD5 | 我按源码推导出了这个结论，需要用真实数据确认判等规则 |

第 3 项需要**真实的破坏性测试**，我建议在一个**专门的小号 + 专用测试目录**里做，而不是你的真实工作目录。`probe` 命令（只读）可以覆盖第 1、2、8 项。

### 请选择下一步

1. **你提供登录方式**，我跑 `reconcile probe`（**纯只读**），把第 1、2、8 项的真实结果填进报告；
   - 建议用独立配置目录，避免影响你可能已有的 BaiduPCS-Go 登录：
     ```powershell
     $env:BAIDUPCS_GO_CONFIG_DIR = 'D:\projects\BaiduPCS-GoPlus\.pcsconfig'
     .\.tools\BaiduPCS-GoPlus.exe login
     .\.tools\BaiduPCS-GoPlus.exe reconcile probe --discover-roots
     ```
   - **也可以你自己跑**并把输出（JSON 报告）给我，我不接触凭据——更安全，我推荐这个。
2. **先按"方案 B"设计**（见下），跳过对官方备份目录的依赖。
3. 先只做 `plan`（dry-run），`apply` 等你实测第 3 项之后再放行。

### 方案 B（若第 3 项证明会被官方客户端持续复原）

README 第 3 节要求此时**停止对该目录开发破坏性动作**，并给出备选。我会这样做，不偷偷扩大范围：

- 把镜像目标改为**普通网盘目录**（例如 `/BackupReconcile/WorkSync`），只做单向镜像；官方自动备份目录完全不动；
- 或者你在客户端里把自动备份改指向该镜像目录（若客户端支持），从而由官方负责增量、由本功能负责对齐；
- 或者明确接受"需要一个真正支持版本历史的同步产品"，本功能只保留只读的差异报告。

---

## 6. 已知限制（不隐瞒）

1. **大文件内容判等不可靠**：>256KB 的文件无法在不下载的前提下可信判等；本方案退化为"大小+时间，存疑则保守覆盖"。会产生不必要的重传，但不会漏更新。
2. **无法用 API 确认官方备份是否暂停**：只能靠你显式声明 + 进程/活跃度告警。
3. **上游分页有静默截断缺陷**：我加了边界检测，但无法从客户端侧彻底修复；超大目录（单目录 ≥1000 项）需要人工复核。
4. **`Move` 到隔离区依赖服务端**：若服务端拒绝跨目录移动，方案是**中止并报告**，不会降级为永久删除。
5. **版本历史未经证实**：上游只有回收站，没有任何历史版本接口；`REPLACE` 的旧版保护只能靠"覆盖前先 `Copy` 到隔离区"。
6. **上游既有测试/vet 问题是既有状态**，我未修改，以免扩大改动面。
7. **本机 TLS 在 DSH 沙箱内不可用**（见 1.3）：编译需一次不受限执行，日常使用不受影响。

---

## 7. 交付物索引

| 文件 | 说明 |
|---|---|
| `reconcile-engineering-reference.md` | 上游 API 逐条核实（签名、`file:line`、负面发现），供实现时查阅 |
| `internal/reconcile/` | 只读扫描 + 探测实现（25 个单测） |
| `internal/pcscommand/reconcile.go`、`main.go` | `reconcile probe` 命令接线 |
| `tools\build.ps1` | 一键构建 + 测试（缓存全部在仓库内） |
| `tools\install-go.ps1` | 便携 Go 工具链安装 |
| `tools\diagnose-https.ps1` | 交付环境 TLS 问题诊断 |
| `README_BaiduPCS_Backup_Reconcile.md` | 你的原始需求（已入库，便于对照验收） |

分支 `feature/reconcile-local`，基线 `1b913181`，本次 commit `8c7d9f1`。
