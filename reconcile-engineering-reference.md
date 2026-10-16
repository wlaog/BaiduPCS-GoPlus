# BaiduPCS-Go (qjfoidnh fork, v4.0.2-dev) 工程参考 — 为新增 `reconcile` 子命令

代码根: `D:\projects\BaiduPCS-GoPlus\upstream`
模块路径: `github.com/qjfoidnh/BaiduPCS-Go` （`go.mod:1`）
Go 版本: `go 1.23` （`go.mod:3`）
版本常量: `Version = "v4.0.2-dev"` （`main.go:58`）

> 所有 `file:line` 均指 `upstream/` 下的文件。本文档未修改任何文件。

---

## 0. 结论速览（写代码前必须知道的 8 条）

| # | 事实 | 位置 |
|---|---|---|
| 1 | 远程 API **没有任何接口抽象**，`baidupcs.BaiduPCS` 是唯一实现，全是具体 struct 方法 | 全仓 `interface {` 搜索结果见 §6.2 |
| 2 | 列目录入口 `FilesDirectoriesList` **不读缓存**；只有 `CacheFilesDirectoriesList` 读缓存（TTL 1 分钟） | `baidupcs/file_directory.go:208`, `baidupcs/cache.go:22` |
| 3 | 缓存 key 格式**不一致**：`deleteCache` 用 `` path+"_"+defaultOrderOptionsStr ``，写入用 `` path+"_"+By+Order ``，所以删除几乎打不中 | `baidupcs/cache.go:13` vs `:23` |
| 4 | 上传单文件到精确路径的最上层现成入口是 `pcscommand.RunUpload`（它不是"精确路径"语义，且不支持 dry-run）；要精确控制需自己组装 `pcsupload.UploadTaskUnit` | `internal/pcscommand/upload.go:54`, `internal/pcsfunctions/pcsupload/upload_task_unit.go:28` |
| 5 | 同名覆盖由 policy 字符串 `skip`/`overwrite`/`rsync` 贯穿，最终变成 PCS 的 `rtype` = `"2"`/`"3"` | `baidupcs/util.go:20-34` |
| 6 | `CheckIsdir` 只能防"覆盖目录"，**不能防"写进不存在的父目录"**；`Mkdir` 不保证创建中间目录 | `baidupcs/util.go:50`, `baidupcs/rm_mkdir.go:28` |
| 7 | `MD5` 字段**不可信**；可靠信号是 `len(BlockList)==1` 时的 `BlockList[0]`，且列表路径会再经 `DecryptMD5` | `baidupcs/file_directory.go:335`, `:384-389`, `baidupcs/util.go:142` |
| 8 | 项目里**没有 crypto/hash 通用 helper** 可用于"本地文件 md5 对比"，只有 `pcsutil/checksum` 的增量校验器 | §1.7 |

---

## 1. 远程文件系统 API 面（package `baidupcs`）

### 1.1 列目录 / 递归列目录

```go
// baidupcs/file_directory.go:208
func (pcs *BaiduPCS) FilesDirectoriesList(path string, options *OrderOptions) (data FileDirectoryList, pcsError pcserror.Error)

// baidupcs/file_directory.go:286 (私有)
func (pcs *BaiduPCS) recurseList(path string, depth int, options *OrderOptions, prebase string,
    handleFileDirectoryFunc HandleFileDirectoryFunc) (fdl FileDirectoryList, ok bool)

// baidupcs/file_directory.go:314
func (pcs *BaiduPCS) FilesDirectoriesRecurseList(path string, options *OrderOptions,
    handleFileDirectoryFunc HandleFileDirectoryFunc) (data FileDirectoryList)
```

**分页行为**（这是本 fork 自己修的，非常关键）：

- `FilesDirectoriesList` 走的是 `pan.baidu.com/api/list`（**不是** PCS `file/list`），见 `baidupcs/prepare.go:166` 的注释与实现：
  ```go
  // baidupcs/prepare.go:160-198
  // 走 pan.baidu.com/api/list 接口: PCS 的 file/list 单次最多返回 1000 条且 num/page 参数被忽略
  // num: 每页条目数 (<=0 或 >1000 时取 1000); page: 页码 (从 1 开始)
  func (pcs *BaiduPCS) PrepareFilesDirectoriesList(path string, options *OrderOptions, num, page int) (io.ReadCloser, pcserror.Error)
  ```
- 分页常量 `const maxListNum = 1000` （`file_directory.go:159`）。
- `FilesDirectoriesList` 自动循环 `page=1..maxPage`（`maxPage = 2000`，`file_directory.go:210`），**每次循环调用方传 `maxListNum`**；用 `map[int64]struct{} seen` 按 `FsID` 去重：
  - 遇到重复 `FsID` → 直接 `return`（返回**已经累积的部分数据 + nil 错误**，不报错！`file_directory.go:238-241`）；
  - `len(jsonData.List) == 0` → break（`:231`）；
  - `len(jsonData.List) < maxListNum` → break，视为最后一页（`:249-251`）。
- 因此：**超过 2000×1000 条的目录会被静默截断**；分页异常时也会静默返回部分结果。写 `reconcile` 时若要"确定性"，建议对返回条数做自检，或直接用 `FilesDirectoriesList` 返回的切片判断。
- `path == ""` → 视为 `PathSeparator`（`"/"`）。`options == nil` 时在 `PrepareFilesDirectoriesList` 内取 `DefaultOrderOptions`（`prepare.go:168-170`）。

**`FileDirectory` 字段**（`baidupcs/file_directory.go:42-58`）：

```go
type FileDirectory struct {
    FsID     int64  // fs_id
    AppID    int64  // app_id
    Path     string // 路径 (网盘绝对路径)
    Filename string // 文件名 或 目录名
    Ctime    int64  // 创建日期 (unix 秒)
    Mtime    int64  // 修改日期 (unix 秒)
    MD5      string // md5 值  ← 见 §1.3，不可信
    BlockListJSON           // 内嵌: BlockList []string `json:"block_list"`
    Size        int64  // 文件大小 (目录为0)
    Isdir       bool   // 是否为目录
    Ifhassubdir bool   // 是否含有子目录 (只对目录有效)
    PreBase     string // 真正的base目录 (仅 recurseList 回填)

    Parent   *FileDirectory    // 仅递归接口内部使用
    Children FileDirectoryList // 仅递归接口内部使用
}
type FileDirectoryList []*FileDirectory
```

- **列表接口（pan）填的字段**：`FsID, AppID, Path, Filename, Ctime(server_ctime), Mtime(server_mtime), MD5, BlockList, Size, Isdir, Ifhassubdir`。映射见 `fdPanJSON.toFileDirectory()` `file_directory.go:178-195`。
- **注意 pan 接口没有 `app_id`/`ifhassubdir` 的官方保证**，代码按同名 JSON key 解析（`:163-175`）。
- `PreBase` **仅在 `recurseList` 里回填**（`:294`）：`prebase` 初值是 `filepath.Base(path)`，向下一层用 `filepath.Join(prebase, filepath.Base(fdl[k].Path))`（`:304`）。`FilesDirectoriesList` 返回的对象 `PreBase` 恒为 `""`。
- `Path` 是**网盘绝对路径**（如 `/a/b/c.mp4`）；`Filename` 是最后一段。`reconcile` 建议只信 `Filename` + 自己拼路径，或校验 `path.Base(fd.Path) == fd.Filename`。
- 目录判断：**必须用 `Isdir`**（`fdJSON.IsdirInt == 1` → true，`file_directory.go:192`）。

**递归回调签名**（`file_directory.go:39`）：

```go
type HandleFileDirectoryFunc func(depth int, fdPath string, fd *FileDirectory, pcsError pcserror.Error) bool
```
- 返回 `false` = 停止递归。**返回 `true` 才继续**。
- 回调是先序（pre-order）：`recurseList` 对每个条目先回调，再对 `Isdir` 条目递归（`:293-308`）。
- **错误通过回调传递**：`FilesDirectoriesRecurseList` 在 meta 失败时立刻 `handleFileDirectoryFunc(0, path, nil, pcsError)` 并把返回值当 ok（`:314-319`）——注意它把回调返回值当成 "ok"，所以回调返回 `true` 时不会中断，但顶层 `data` 是 nil。
- 已知缺陷：`recurseList` 的 `ok :=` 遮蔽（`:289`），以及 `FilesDirectoriesRecurseList` 在 meta 说有子目录但 list 出错时只把错误丢给回调。
- 用途示例：`internal/pcscommand/download.go:107-121`（收集 `file_dir_list`）、`internal/pcscommand/export.go`。

**其他相关**：

```go
// baidupcs/file_directory.go:110
func (pcs *BaiduPCS) FilesDirectoriesMeta(path string) (data *FileDirectory, pcsError pcserror.Error)
// baidupcs/file_directory.go:132
func (pcs *BaiduPCS) FilesDirectoriesBatchMeta(paths ...string) (data FileDirectoryList, pcsError pcserror.Error)
// baidupcs/file_directory.go:257
func (pcs *BaiduPCS) Search(targetPath, keyword string, recursive bool) (fdl FileDirectoryList, pcsError pcserror.Error)
```
- `FilesDirectoriesMeta` 内部就是 `FilesDirectoriesBatchMeta(path)`，**若返回条数 != 1 直接报 `ErrTypeOthers` "未知返回数据"**（`:120-127`）。**不存在的路径** 走远端错误 → `pcserror.ErrTypeRemoteError`，`GetRemoteErrCode()` 一般是 `-9` 或 `31066`。
- `FilesDirectoriesBatchMeta` 走 PCS `file/meta`（`prepare.go:141`），返回结果已做 `fixMD5()`（`:152`）。
- `FileDirectory` 与 `FileDirectoryList` 的辅助方法：`TotalSize()` `:392`、`Count() (fileN, directoryN int64)` `:410`、`AllFilePaths() []string` `:433`、`String()`（表格渲染，`:342`）。

### 1.2 `OrderOptions`

```go
// baidupcs/file_directory.go:93
type OrderOptions struct { By OrderBy; Order Order }
// baidupcs/file_directory.go:24-35
OrderByName OrderBy = "name"; OrderByTime = "time"; OrderBySize = "size"
OrderAsc Order = "asc";  OrderDesc Order = "desc"
// baidupcs/file_directory.go:99-107
var DefaultOrderOptions = &OrderOptions{By: OrderByName, Order: OrderAsc}
var defaultOrderOptionsStr = fmt.Sprint(DefaultOrderOptions)
```
- `DefaultOrderOptions` 是**可变全局指针**。`reconcile` 不要改它；需要稳定顺序（保证报告可复现）时自己构造 `&baidupcs.OrderOptions{By: baidupcs.OrderByName, Order: baidupcs.OrderAsc}`（注意会改变缓存 key，见 §5）。

### 1.3 元信息 / 单文件 hash：`MD5` 可不可信？

**明确回答：单独的 `MD5` 字段不可信。**

- 定义处注释是"md5 值"，但项目自己在多处打补丁：
  - `fixMD5()` `file_directory.go:335-340`：当 `len(BlockList) == 1` 时，**用 `BlockList[0]` 覆盖 `MD5`**。
  - `FileDirectoryList.fixMD5()` `file_directory.go:384-389`：先逐项 `fixMD5()`，再对每一项执行 `v.MD5 = DecryptMD5(v.MD5)`。
  - `DecryptMD5(rawMD5 string) string` `baidupcs/util.go:142-166`：把服务端"加密/位移"过的 32 位 hex 还原；非 32 长度或第 10 位已是 `[a-f0-9]` 则原样返回（`:143-150`）。
- 调用点：`FilesDirectoriesBatchMeta`（列表/元信息）`:152`；`FilesDirectoriesList` **逐页** `pageList.fixMD5()` `:246`；`Search` `:280`。所以**从这三个 API 拿到的 `MD5` 已经过 fix + decrypt**。
- 仍不可信的三种情形（代码自己的注释）：
  1. `len(BlockList) > 1` → MD5 可能错。UI 明确标注 `md5 (可能不正确)`：`file_directory.go:355-356`、`internal/pcscommand/ls_search.go:102-106`。
  2. 分片上传之后服务端可能记录错误 md5（`main.go:1183` 的 upload 说明，以及 `fixmd5` 子命令的存在）。
  3. 上传代码里的 TODO：`internal/pcsfunctions/pcsupload/upload_task_unit.go:192` `// TODO: fd.MD5 有可能是错误的`。
- 秒传场景用 `len(BlockList) == 1` 作为"可信且完整"的信号（`file_directory.go:336`）；`FixMD5ByFileInfo` 的跳过条件也是 `len(finfo.BlockList) == 1 && finfo.BlockList[0] == finfo.MD5`（`baidupcs/extends.go:290-293`）。
- **`BlockListJSON`**（`baidupcs/jsontable.go:48-51`）：`type BlockListJSON struct { BlockList []string \`json:"block_list"\` }`，内嵌在 `fdJSON`/`fdPanJSON`/`FileDirectory` 里。语义是"文件按分块大小切分后每块的 md5"，长度 > 1 表示文件被分块，此时顶层 `md5` 字段可能是分片 md5 的某种汇总而非整文件 md5。

**`reconcile` 的实用建议**：把"远端已存在且大小相同"作为弱相等；若必须用 hash，仅在 `len(fd.BlockList) == 1` 时用 `fd.BlockList[0]`（等价于 `fd.MD5`，因为 `fixMD5` 已覆盖），否则退化为 size 比较或用 `FixMD5ByFileInfo` 主动修复后再读。

### 1.4 单文件 hash 修复

```go
// baidupcs/extends.go:271
func (pcs *BaiduPCS) FixMD5ByFileInfo(finfo *FileDirectory) (pcsError pcserror.Error)
// baidupcs/extends.go:322
func (pcs *BaiduPCS) FixMD5(pcspath string) (pcsError pcserror.Error)
// 错误哨兵: baidupcs/extends.go:26-33
ErrFixMD5Isdir / ErrFixMD5Failed / ErrFixMD5FileInfoNil / ErrMatchPathByShellPatternNotAbsPath
```
实现路径：`getLocateDownloadLink` → `GetRapidUploadInfoByLink` → `RapidUploadNoCheckDir`（`extends.go:295-318`）。**会真正调用秒传接口改写远端记录**，`reconcile` 不应默认调用（有副作用）。`internal/pcscommand/fixmd5.go:9` 是它的命令层包装。

### 1.5 上传：`baidupcs` 与 `pcsupload` 两层

#### (a) `baidupcs` 低层常量（`baidupcs/upload.go:14-35`）

```go
MaxUploadBlockSize           = 64 * converter.MB
MiddleUploadBlockSize        = 16 * converter.MB
MinUploadBlockSize           = 4  * converter.MB
RecommendedUploadSize        = 32 * converter.GB   // 超过只警告
MaxUploadSize                = 128 * converter.GB  // 硬上限，超过拒绝
SliceMD5Size                 = 256 * converter.KB
EmptyContentMD5              = "d41d8cd98f00b204e9800998ecf8427e"
MiddleUploadThreshold        = 8  * converter.GB   // >= 用 16MB 分片
MaxUploadThreshold           = 32 * converter.GB   // >= 用 64MB 分片
MinCheckLeftSpaceThreshold   = 64 * converter.MB   // 超过才检查剩余空间
```
分片大小选择逻辑（**真正的规则在这里**）：
```go
// internal/pcsfunctions/pcsupload/utils.go:11-20
func getBlockSize(fileSize int64) int64 {
    blockSize := baidupcs.MinUploadBlockSize            // 默认 4MB
    if fileSize >= baidupcs.MiddleUploadThreshold { blockSize = baidupcs.MiddleUploadBlockSize } // 16MB
    if fileSize >= baidupcs.MaxUploadThreshold    { blockSize = baidupcs.MaxUploadBlockSize }    // 64MB
    return blockSize
}
```
（`getBlockSize` 是**小写未导出**，`reconcile` 若在 `pcsupload` 包外无法直接调用，需复制这 6 行或把上传走 `UploadTaskUnit`。）

#### (b) `baidupcs` 上传方法（都返回 `pcserror.Error`）

```go
// baidupcs/upload.go:105
func (pcs *BaiduPCS) RapidUpload(targetPath, policy, uploadid, contentMD5, sliceMD5, dataContent, crc32 string,
    offset, length, totalSize, dataTime int64, blockListMD5 []string) (pcsError pcserror.Error, jsonData uploadPrecreateJSON)
// baidupcs/upload.go:117
func (pcs *BaiduPCS) FakeRapidUpload(targetPath, policy string, length int64) (pcsError pcserror.Error, jsonData uploadPrecreateJSON)
// baidupcs/upload.go:159  ← 危险：会覆盖同名目录
func (pcs *BaiduPCS) RapidUploadNoCheckDir(targetPath, contentMD5, sliceMD5, crc32 string, length int64) (pcsError pcserror.Error)
// baidupcs/upload.go:176
func (pcs *BaiduPCS) UploadTmpFile(uploadid, targetPath string, partseq int, partOffset int64, uploadFunc UploadFunc) (md5 string, pcsError pcserror.Error)
// baidupcs/upload.go:205
func (pcs *BaiduPCS) UploadCreateSuperFile(uploadid, policy string, fileSize int64, targetPath string, checksumMap map[int]string) (panError pcserror.Error)
// baidupcs/upload.go:226
func (pcs *BaiduPCS) GetRandomPCSHost() (pcsError pcserror.Error, pcsHost string)
```
- `uploadPrecreateJSON`（`:71-77`）含 `ReturnType int`（**1=需要真实上传，2=秒传成功**）、`UploadID string`、内嵌 `fdJSON` `json:"info"`。
- `policyTortype`（`:26-34`）：`skip → "2"`，其它一律 → `"3"`（**兜底是覆盖**）。`UploadCreateSuperFile` 用它作为 `rtype`（`:207`）。
- 缓存更新：`RapidUpload`/`FakeRapidUpload` 成功后 `deleteCache([]string{path.Dir(targetPath)})`（`:106-111`, `:118-123`）；`UploadCreateSuperFile` 同样（`:220-222`）。
- `UploadTmpFile` 缺 md5 时构造 `ErrTypeInternalError` + `ErrUploadMD5NotFound`（`:195-199`）。
- 准备好的错误哨兵：`upload.go:38-50` `ErrUploadMD5NotFound` / `ErrUploadSavePathFound` / `ErrUploadSeqNotMatch` / `ErrUploadMD5Unknown` / `ErrUploadFileExists`，以及 `fakeBlockListMD5`。

#### (c) `prepare.go` 中对应的 Prepare*（返回 `(io.ReadCloser, pcserror.Error)`）

`prepare.go:334` `prepareRapidUpload`、`:360` `prepareRapidUploadV2`、`:396` `prepareFakeRapidUploadV2`、`:421` `PrepareRapidUpload`、`:432` `PrepareRapidUploadV2`、`:442` `PrepareFakeRapidUploadV2`、`:526` `PrepareUpload`、`:554` `PrepareUploadTmpFile`、`:580` `PrepareUploadCreateSuperFile`、`:605` `PrepareUploadPrecreate`、`:631` `PrepareUploadSuperfile2`。
`PrepareRapidUpload`(`:421`) 与 `PrepareRapidUploadV2`(`:432`) 与 `PrepareFakeRapidUploadV2`(`:442`) 里都调用 `pcs.CheckIsdir(...)`（`:423`, `:434`, `:444`）作为**覆盖目录的守卫**。

#### (d) `internal/pcsfunctions/pcsupload` — 高层上传

**`reconcile` 应该用哪个？**

```go
// internal/pcsfunctions/pcsupload/upload.go:52
func NewPCSUpload(pcs *baidupcs.BaiduPCS, targetPath string) uploader.MultiUpload
// internal/pcsfunctions/pcsupload/upload_task_unit.go:28
type UploadTaskUnit struct { /* 见下 */ }
// internal/pcsfunctions/pcsupload/upload_task_unit.go:474
func (utu *UploadTaskUnit) Run() (result *taskframework.TaskUnitRunResult)
```
`NewPCSUpload` 只是把上传"传输层"适配成 `uploader.MultiUpload` 接口（`requester/uploader/multiuploader.go:17-21`）：

```go
type MultiUpload interface {
    Precreate() (pcsHost string, err pcserror.Error)
    TmpFile(ctx context.Context, uploadid, targetPath string, partseq int, partOffset int64, readerlen64 rio.ReaderLen64) (checksum string, terr error)
    CreateSuperFile(pcsHost, policy, uploadId string, fileSize int64, checksumMap map[int]string) (cerr error)
}
```
整条上传链路由 `uploader.MultiUploader` 驱动：
```go
// requester/uploader/multiuploader.go:61
func NewMultiUploader(multiUpload MultiUpload, file rio.ReaderAtLen64, config *MultiUploaderConfig, targetPath string) *MultiUploader
// requester/uploader/multiuploader.go:52-57
type MultiUploaderConfig struct {
    Parallel  int    // 上传并发量
    BlockSize int64  // 上传分块
    MaxRate   int64  // 限制最大上传速度
    Policy    string // 文件重名策略  ← policy 就是这样传下去的
}
```
policy 的最终归宿：`MultiUploader.Execute` → `multiworker.go:155`
```go
cerr := muer.multiUpload.CreateSuperFile(originPCSHost, muer.config.Policy, muer.instanceState.Uploadid, muer.file.Len(), checksumMap)
```
→ `PCSUpload.CreateSuperFile`（`pcsupload/upload.go:126`）→ `pu.pcs.UploadCreateSuperFile(...)` → `policyTortype` → `"2"`/`"3"`。

**`UploadTaskUnit` 全字段**（`upload_task_unit.go:28-49`）：
```go
type UploadTaskUnit struct {
    LocalFileChecksum *checksum.LocalFileChecksum // 要上传的本地文件详情
    Step              StepUpload
    SavePath          string // 保存路径（网盘绝对路径，包含文件名！）
    PrintFormat       string

    PCS               *baidupcs.BaiduPCS
    UploadingDatabase *UploadingDatabase // 断点续传数据库
    Parallel          int
    NoRapidUpload     bool   // 禁用秒传
    NoSplitFile       bool   // 禁用分片上传（注意：字段存在但 Run() 里未真正生效于 MultiUploaderConfig）
    Policy            string // 上传重名文件策略

    UploadStatistic *UploadStatistic
    // 私有: taskInfo, panDir, panFile, state, prepareResult
}
```
`Run()` 流程（`upload_task_unit.go:474-525`）：
1. `Length > baidupcs.MaxUploadSize` → 直接失败返回（`:477-486`）。
2. `LocalFileChecksum.OpenPath()` 失败 → 失败返回（`:488-495`）——**这是"文件不可读"的现成测试点**。
3. `prepareFile()`（`:74-142`）：`path.Split(SavePath)` 得到 `panDir`/`panFile`；查断点续传库；`Length > MinCheckLeftSpaceThreshold` 时 `PCS.SpaceLeftInfo()` 检查剩余空间（`:99-110`）；`NoRapidUpload` → `FakeRapidUpload`；否则 `Step = StepUploadRapidUpload`。
4. `rapidUpload()`（`:145-292`）：先 `PCS.CacheFilesDirectoriesList(panDir, DefaultOrderOptions)`（`:151`，**注意这里用的是缓存接口！**），按 `fd.Filename == panFile` 找同名；命中且（policy==skip 或 md5 相等）→ 直接成功跳过（`:188-200`）。然后 `CacheUK` → `creaetDataOffset` → `GetSliceDataContent` → `CalculateChunkedSum(blockSize)` → `PCS.RapidUpload(...)`。`jsonData.ReturnType == 2` 即秒传成功（`:240-246`）。
5. `upload()`（`:295-432`）：`uploader.NewMultiUploader(NewPCSUpload(utu.PCS, utu.SavePath), rio.NewFileReaderAtLen64(...), &uploader.MultiUploaderConfig{Parallel, BlockSize: getBlockSize(Length), MaxRate: pcsconfig.Config.MaxUploadRate, Policy}, utu.SavePath)`，然后 `RegisterActiveUploader` / `OnUploadStatusEvent` / `OnSuccess` / `OnError` → `muer.Execute()`。

**用 `reconcile` 上传单个本地文件到精确远端路径、支持覆盖的推荐做法**：
- 最高层现成函数 `pcscommand.RunUpload`（`internal/pcscommand/upload.go:54`）：
  ```go
  type UploadOptions struct {           // upload.go:28-36
      Parallel        int
      MaxRetry        int
      Load            int
      NoRapidUpload   bool
      NoSplitFile     bool   // 禁用分片上传
      Policy          string // 同名文件处理策略
      NoFilenameCheck bool   // 禁用文件名合法性检查
  }
  func RunUpload(localPaths []string, savePath string, opt *UploadOptions) error
  ```
  语义是"本地路径列表 → **目标目录**"，内部 `WalkDir` 递归 + `strings.TrimPrefix` 计算相对路径再 `path.Clean(savePath + "/" + subSavePath)`（`upload.go:126-171`），靠 `policy` 决定重名行为，**没有"精确目标文件路径"参数，也没有 dry-run**，且它会注册 signal handler、打印进度、打开断点续传数据库。
- 若 `reconcile` 需要"本地文件 → 精确远端文件路径 + dry-run"，**要么**自己组装 `pcsupload.UploadTaskUnit{LocalFileChecksum: checksum.NewLocalFileChecksum(local, int(baidupcs.SliceMD5Size)), SavePath: <精确路径>, PCS: GetBaiduPCS().CopyPCS(), Policy: baidupcs.OverWritePolicy, ...}` 并用 `taskframework.TaskExecutor.Append/Run`；**要么**只调用 `baidupcs` 的 `RapidUpload`/`UploadCreateSuperFile`+`NewPCSUpload` 自己拼；**没有**现成的"单文件精确上传"helper —— **这是明确的负面发现**。
- 并发/速率默认值来源：`pcsconfig.Config.MaxUploadParallel`（默认 4）、`MaxUploadLoad`（默认 4）、`MaxUploadRate`（默认 0=不限），见 `internal/pcsconfig/pcsconfig.go:235-237` 与 `:244`。
- `NoSplitFile` 在 `Run()` 里**没有被真正使用**（`upload_task_unit.go:38` 定义、`pcscommand/upload.go:162` 赋值，但 `upload()` 里没有分支）—— 潜在坑。
- 上传客户端是**包级单例 + `sync.Once`**：`upload.go:28-40`
  ```go
  var uploadClient *requester.HTTPClient
  var uploadClientOnce sync.Once
  func getUploadClient(jar http.CookieJar) *requester.HTTPClient  // 200s timeout，jar 只设一次
  ```
  这意味着**第一次调用时的 jar 会被永久复用**；测试里跨账号切换会串 cookie。
- 全局"优雅停止"状态：`active.go:9-14` `activeUploaders sync.Map` + `uploadStopping int32`；`GracefulStopActiveUploaders()` `:18`、`RegisterActiveUploader()` `:28`、`UnregisterActiveUploader()` `:36`。测试若调用过 stop，全局标志不会复位。
- 断点续传数据库：`NewUploadingDatabase()`（`upload_database.go:35`）把文件开在 `filepath.Join(pcsconfig.GetConfigDir(), UploadingFileName)`，`UploadingFileName = "pcs_uploading.json"`（`pcsupload.go:9`）。**`reconcile` 若不想污染断点库可传入自定义 `UploadingDatabase`，但没有 nil-safe 实现**（`rapidUpload` 会直接 `utu.UploadingDatabase.Save()`）。

### 1.6 创建目录 / 移动 / 重命名 / 拷贝 / 删除

```go
// baidupcs/rm_mkdir.go:9
func (pcs *BaiduPCS) Remove(paths ...string) (pcsError pcserror.Error)
// baidupcs/rm_mkdir.go:28
func (pcs *BaiduPCS) Mkdir(pcspath string) (pcsError pcserror.Error)

// baidupcs/cp_mv_rename.go:9
func (pcs *BaiduPCS) Rename(from, to string) (pcsError pcserror.Error)
// baidupcs/cp_mv_rename.go:17
func (pcs *BaiduPCS) Copy(cpmvJSON ...*CpMvJSON) (pcsError pcserror.Error)
// baidupcs/cp_mv_rename.go:22
func (pcs *BaiduPCS) Move(cpmvJSON ...*CpMvJSON) (pcsError pcserror.Error)
// baidupcs/cp_mv_rename.go:26
func (pcs *BaiduPCS) cpmvOp(op string, cpmvJSON ...*CpMvJSON) (pcsError pcserror.Error) // 私有
```
参数结构体（`baidupcs/jsontable.go:34-46`）：
```go
type CpMvJSON struct {
    From string `json:"from"` // 源文件或目录
    To   string `json:"to"`   // 目标文件或目录
}
type CpMvJSONList []*CpMvJSON
type CpMvListJSON struct { List CpMvJSONList `json:"list"` }
```
辅助（`jsontable.go:74` `(*CpMvJSON).JSON()`、`:80` `(*CpMvListJSON).JSON()`、`:85` `String()` 表格、`:103` `(*CpMvJSONList).AllRelatedDir() []string`）。

- 实现：`prepare.go:283 prepareCpMvOp(op, ...)` 把 op 映射为 `copy` / `move`（`OperationRename` 也走 `move`，`:289`），走 `generatePCSURL("file", method)`（`:303`）。Rename 是"move 到新路径"的语法糖（`cp_mv_rename.go:10-14`）。
- 缓存失效：`cpmvOp` 末尾 `pcs.deleteCache((*CpMvJSONList)(unsafe.Pointer(&cpmvJSON)).AllRelatedDir())`（`:40`）—— 依赖 slice header 与 `CpMvJSONList` 的内存布局一致（`unsafe`），**脆弱**。
- `Remove` 末尾 `pcs.deleteCache(allRelatedDir(paths))`（`rm_mkdir.go:23`）；`allRelatedDir` 定义在 `util.go:115-123`（只取 `path.Dir`，去重）。
- `Mkdir` 末尾 `pcs.deleteCache([]string{path.Dir(pcspath)})`（`rm_mkdir.go:42`）。`PrepareMkdir`（`prepare.go:272`）只发一个 `path` 参数到 PCS `file/mkdir`。

**关于"move 是否创建中间目录" —— 明确回答：仓库里没有任何代码创建中间目录。**
- 全仓 `Mkdir(` 调用点只有 4 处：定义 `baidupcs/rm_mkdir.go:28`、`prepare.go:272`、命令层 `internal/pcscommand/rm_mkdir.go:42`、以及 `internal/pcscommand/transfer.go:110`（转存时**只 mkdir 一层**目录名）。
- 没有任何循环 `path.Dir` 逐级 mkdir 的代码；是否由服务端隐式创建父目录**在仓库内无法证实**。`reconcile` 若要建深层目录树，必须自己按 `path.Dir` 逐级 `Mkdir`（并容忍"已存在"错误），或先 `FilesDirectoriesMeta` 探测。
- `CheckIsdir`（`util.go:50`）只在"秒传/上传"路径被调用（`prepare.go:423,434,444`），**mkdir/move/copy 路径没有目录冲突检查**。

### 1.7 回收站

```go
// baidupcs/recycle.go:47
func (pcs *BaiduPCS) RecycleList(page int) (fdl RecycleFDInfoList, panError pcserror.Error)
// baidupcs/recycle.go:69
func (pcs *BaiduPCS) RecycleRestore(fidList ...int64) (sussFsIDList []*FsIDJSON, pcsError pcserror.Error)
// baidupcs/recycle.go:87
func (pcs *BaiduPCS) RecycleDelete(fidList ...int64) (panError pcserror.Error)
// baidupcs/recycle.go:100
func (pcs *BaiduPCS) RecycleClear() (sussNum int, pcsError pcserror.Error)
```
- `RecycleFDInfo`（`recycle.go:9-19`）：`FsID int64`、`Isdir int`（**int 不是 bool**）、`LeftTime int`、`Path`、`Filename`、`Ctime(server_ctime)`、`Mtime(server_mtime)`、`MD5`、`Size`。**没有 `fixMD5`/`DecryptMD5` 处理**，`MD5` 更不可信。
- 分页靠调用方传 `page`（`PrepareRecycleList(page)` `prepare.go:824`），**没有自动翻页**。
- 命令层：`internal/pcscommand/recycle.go:15 RunRecycleList(page int)`、`:42 RunRecycleRestore(fidStrList ...string)`、`:63 RunRecycleDelete(...)`、`:78 RunRecycleClear()`。
- `reconcile` 若提供 `--recycle` 删除策略，用 `Remove()`（进回收站，可恢复）而不是 `RecycleDelete`（彻底删除）。

### 1.8 `pcserror` 错误类型与重试/中止决策

**接口与类型值**（`baidupcs/pcserror/pcserror.go`）：
```go
// :14-24
type Error interface {
    error
    SetJSONError(err error)
    SetNetError(err error)
    SetRemoteError()
    GetOperation() string
    GetErrType() ErrType
    GetRemoteErrCode() int
    GetRemoteErrMsg() string
    GetError() error
}
// :27-40
type ErrType int
const (
    ErrorTypeNoError ErrType = iota   // 0
    ErrTypeInternalError              // 1  内部错误
    ErrTypeRemoteError                // 2  远端服务器返回错误
    ErrTypeNetError                   // 3  网络错误
    ErrTypeJSONParseError             // 4  json 解析失败
    ErrTypeOthers                     // 5  其他错误
)
```
两个具体实现（都是 `Error`）：
- `*pcserror.PCSErrInfo` — `pcserrorinfo.go:9-15`，字段 `Operation, ErrType, Err, ErrCode int \`json:"error_code"\`, ErrMsg string \`json:"error_msg"\``。远端错误码来自 `ErrCode`（`:54-56`）。已知码翻译在 `findPCSErr` `:103-116`：`31045`（登录过期）、`31061`（文件已存在）、`31066`（文件或目录不存在）、`31079`（秒传失败）。
- `*pcserror.PanErrorInfo` — `panerrorinfo.go:9-14`，字段 `Operation, ErrType, Err, ErrNo int \`json:"errno"\``。远端码来自 `ErrNo`（`:53-55`），翻译表 `FindPanErr` `:101-178`（如 `-6` 请重新登录、`-8` 已存在同名文件、`-9` 文件不存在、`-30` 文件已存在、`3` 未登录）。
- 第三类 `*XPanErrorInfo` 在 `xpanerrorinfo.go`（下载直链相关）。

**判定与构造**：
- `HandleJSONParse(op, data, info)` `pcserror.go:74-95`：JSON 失败 → `ErrTypeJSONParseError`；`GetRemoteErrCode() != 0` → `SetRemoteError()`。
- `DecodePCSJSONError` `:56`、`DecodePanJSONError` `:62`、`DecodeXPanJSONError` `:68`。
- **网络层错误**：`baidupcs/prepare.go:69-87` —— `pcs.client.Req` 出错时按 reqType 返回 `ErrTypeNetError`；HTTP 4xx/5xx 走 `handleRespStatusError`（`prepare.go:36-49`）→ `SetRemoteError()`（**注意：HTTP 状态错误被归类为 Remote，不是 Net**）。

**重试/中止的既有约定（照抄这个模式）** —— 最好的范本是 `internal/pcsfunctions/pcsupload/upload_task_unit.go:354-428`：
```go
result.NeedRetry = true                      // 默认重试
switch pcsError.GetErrType() {
case pcserror.ErrTypeRemoteError:
    switch pcsError.GetRemoteErrCode() {
    case 114514: /* skip 策略下的自定义码: 跳过, 不重试 */  result.NeedRetry = false
    case 1919810: /* rsync 策略下的自定义码: 大小未变, 跳过 */ result.NeedRetry = false
    case 31363:  /* block miss in superfile2, 上传状态过期 → 需要重试 */
    case 31061:  /* 已存在重名文件, 不重试 */
    default:     /* 其他远端错误: 保留 NeedRetry=true */
    }
case pcserror.ErrTypeNetError:
    if strings.Contains(pcsError.GetError().Error(), "413 Request Entity Too Large") { result.NeedRetry = false }
default:                                     // InternalError / JSONParseError / Others
    result.NeedRetry = false
}
```
另一处范本：`upload_task_unit.go:151-172`（列目录错误：`ErrTypeRemoteError` 且码 `31066/-9` 视为"目录不存在"，否则其它远端错误不重试；非远端错误重试）。

**自定义错误码（项目自造，务必记住）** —— 见 `baidupcs/util.go:68-85`：
```go
case SkipPolicy:   errCode = 114514; ErrType = ErrTypeRemoteError; ErrMsg = "目标位置存在同名文件"
case RsyncPolicy:  if targetFileSize == fileSize { errCode = 1919810; ... "目标位置存在相同文件" }
```
这两个码在 `upload_task_unit.go:117,255,266,371,382` 被显式识别。**`reconcile` 必须先判 `errCode == 114514/1919810` 再当"远端错误"处理，否则会把"跳过"误判为失败。**

### 1.9 `CheckIsdir` 如何防"覆盖目录"

```go
// baidupcs/util.go:36
func (pcs *BaiduPCS) Isdir(pcspath string) (fileSize int64, isdir bool, pcsError pcserror.Error)
// baidupcs/util.go:50
func (pcs *BaiduPCS) CheckIsdir(op string, targetPath string, policy string, fileSize int64) pcserror.Error
```
行为（`util.go:50-87`）：
1. `Isdir(targetPath)`；
2. 若返回错误且 `GetErrType() != ErrTypeRemoteError` → **直接向上返回该错误**（网络/解析错误不吞）；若为远端错误（通常是 -9/31066 不存在）→ **忽略**（`:54-59`）；
3. `isdir == true` → 返回 `ErrTypeOthers` + `errors.New("保存路径不可以覆盖目录")`（`:62-66`）；
4. 目标存在且是文件时按 policy：
   - `SkipPolicy` → `ErrCode=114514`, `ErrType=ErrTypeRemoteError`, `ErrMsg="目标位置存在同名文件"`（`:70-74`）
   - `RsyncPolicy` 且 `targetFileSize == fileSize` → `ErrCode=1919810`（`:75-81`）
   - 默认（`OverWritePolicy`）→ `nil`（允许覆盖）（`:82-84`）
5. `path.Clean(pcspath) == "/"` 时 `Isdir` 短路返回 `(0, true, nil)`（`:38-40`）。

**调用点**：仅 `prepare.go:423`（`PrepareRapidUpload`）、`:434`（`PrepareRapidUploadV2`）、`:444`（`PrepareFakeRapidUploadV2`）。`RapidUploadNoCheckDir`（`upload.go:159`）**故意不调用它**，注释明说"会覆盖掉同名的目录!"—— **`reconcile` 绝不可用 `RapidUploadNoCheckDir`。**

---

## 2. 现有命令的端到端接线（`upload` 与 `ls` 全链路）

### 2.1 `ls` 链路

| 步骤 | 位置 | 内容 |
|---|---|---|
| 命令注册 | `main.go:705-786`（`Name: "ls"` 在 `:706`，`Aliases: ["l","ll"]` `:707`，`Category: "百度网盘"` `:730`） | `Before: reloadFn`（`:731`），`Action` 解析 `-asc/-desc/-time/-name/-size` 成 `*baidupcs.OrderOptions`（`:733-752`） |
| 调用 | `main.go:754-756` | `pcscommand.RunLs(c.Args().Get(0), &pcscommand.LsOptions{Total: c.Bool("l") \|\| c.Parent().Args().Get(0)=="ll"}, orderOptions)` |
| 命令层 | `internal/pcscommand/ls_search.go:33` | `func RunLs(pcspath string, lsOptions *LsOptions, orderOptions *baidupcs.OrderOptions)` |
| 命令层实现 | `ls_search.go:34-53` | ① `matchPathByShellPatternOnce(&pcspath)`（通配符，失败即 `fmt.Println` 后 return）② `GetBaiduPCS().FilesDirectoriesList(pcspath, orderOptions)` ③ `renderTable(opLs, lsOptions.Total, pcspath, files)` |
| baidupcs | `baidupcs/file_directory.go:208` | `FilesDirectoriesList` |

### 2.2 `upload` 链路

| 步骤 | 位置 | 内容 |
|---|---|---|
| 命令注册 | `main.go:1172-1249`（`Name: "upload"` `:1173`，`Aliases: ["u"]` `:1174`，`Category` `:1205`） | `Before: reloadFn` `:1206`；`Action` `:1207-1225`：`c.NArg() < 2` → help；否则 `pcscommand.RunUpload(subArgs[:c.NArg()-1], subArgs[c.NArg()-1], &pcscommand.UploadOptions{...})`；错误 → `cli.NewExitError(err.Error(), 1)` |
| Flags | `main.go:1226-1248` | `-p`(Int, 单文件最大线程数)、`-retry`(Int, 默认 `pcscommand.DefaultUploadMaxRetry`=3 `internal/pcscommand/upload.go:23`)、`-l`(Int, 最大同时上传文件数)、`-norapid`(Bool)、`-policy`(String) |
| 命令层 | `internal/pcscommand/upload.go:54` | `func RunUpload(localPaths []string, savePath string, opt *UploadOptions) error` |
| 命令层实现 | `upload.go:55-198` | 见下 |
| 上传单元 | `internal/pcsfunctions/pcsupload/upload_task_unit.go:474` | `(*UploadTaskUnit).Run()` |
| baidupcs | `baidupcs/upload.go:105/117/176/205` | `RapidUpload` / `FakeRapidUpload` / `UploadTmpFile` / `UploadCreateSuperFile` |

`RunUpload` 内部要点（`internal/pcscommand/upload.go`）：
- 默认值回填：`opt.Parallel <= 0 → pcsconfig.Config.MaxUploadParallel`（`:60-62`）；`opt.NoFilenameCheck = pcsconfig.Config.IgnoreIllegal`（`:64`，**覆盖调用方传的值**）；`opt.MaxRetry < 0 → 3`（`:66-68`）；`opt.Load <= 0 → MaxUploadLoad`（`:70-72`）；policy 非法 → `pcsconfig.Config.UPolicy`（`:74-76`）。
- `savePath` 走**一次**通配符匹配：`matchPathByShellPatternOnce(&savePath)`（`:78`）。
- `os.Interrupt`/`SIGTERM` 信号处理 + `os.Exit(130)`（`:107-117`）—— **写库函数时不要照抄到可复用的命令实现里**。
- 本地遍历 `pcsutil.WalkDir(localPaths[k], "")`（`:127`），Windows 下 `pcsutil.ConvertToUnixPathSeparator`（`:135-140`），非法字符检查 `pcsutil.ChPathLegal`（`:150-152`，可用 `-ignore_illegal` 关闭）。
- `SavePath: path.Clean(savePath + baidupcs.PathSeparator + subSavePath)`（`:156`），`PCS: pcs.CopyPCS()`（`:157`，**每个任务一份客户端副本以隔离 pcsAddr 切换**）。
- `taskframework.TaskExecutor{IsFailedDeque: true}` + `executor.Append(&pcsupload.UploadTaskUnit{...}, opt.MaxRetry)`（`:98-100`, `:154-165`）+ `executor.SetParallel(LoadCount)`（`:179`）+ `executor.Execute()`（`:181`）。
- 失败列表表格：`pcstable.NewTable(os.Stdout)`（`:191-196`）；汇总 `uploadFailureError(failedCount)`（`:198`，定义 `:46-51`）。

### 2.3 新增 `reconcile` 必须遵循的模式

1. **命令层新增一个文件**：`internal/pcscommand/reconcile.go`，导出 `func RunReconcile(localDir, remoteDir string, opt *ReconcileOptions) error`（返回 error 让 CLI 用 `cli.NewExitError`）。
   - 包内已有 `pcsCommandVerbose = pcsverbose.New("PCSCOMMAND")`（`internal/pcscommand/pcscommand.go:11`），调试日志复用它。
   - 取客户端：**只能用** `GetBaiduPCS()`（`pcscommand.go:20-22`，内部 `pcsconfig.Config.ActiveUserBaiduPCS()`）和 `GetActiveUser()`（`:15-17`，内部 `pcsconfig.Config.ActiveUser()`）。**不要在命令层直接读 `pcsconfig.Config.pcs`（未导出）**。
   - 输出用 `os.Stdout` + `pcstable`（见 §7）。
2. **`main.go` 注册命令**：在 `app.Commands = []cli.Command{...}`（`main.go:328` 开始）的数组里插入一个新元素。推荐插在 `upload` 之后（`main.go:1249` 之后、`locate`（`:1250`）之前），保持"百度网盘"分类聚集。最小骨架：
   ```go
   {
       Name:      "reconcile",
       Usage:     "单向镜像: 使网盘目录与本地目录一致",
       UsageText: app.Name + " reconcile [-dry-run] <本地目录> <网盘目录>",
       Category:  "百度网盘",
       Before:    reloadFn,
       Action: func(c *cli.Context) error {
           if c.NArg() < 2 { cli.ShowCommandHelp(c, c.Command.Name); return nil }
           err := pcscommand.RunReconcile(c.Args().Get(0), c.Args().Get(1), &pcscommand.ReconcileOptions{
               DryRun: c.Bool("dry-run"),
               Policy: c.String("policy"),
           })
           if err != nil { return cli.NewExitError(err.Error(), 1) }
           return nil
       },
       Flags: []cli.Flag{ /* cli.BoolFlag{Name: "dry-run", ...}, cli.StringFlag{Name: "policy", ...} */ },
   },
   ```
3. **`Before: reloadFn` 是硬要求**：`main.go:61-67`
   ```go
   reloadFn = func(c *cli.Context) error {
       err := pcsconfig.Config.Reload()   // → PCSConfig.init()
       if err != nil { fmt.Printf("重载配置错误: %s\n", err) }
       return nil                          // 永远返回 nil，不会阻断命令
   }
   ```
   所有需要登录态的命令都带它（如 `ls:731`、`rm:935`、`mkdir:951`、`cp:979`、`mv:1010`、`upload:1206`）；`run`/`env`/`tool` 等本地命令不带（`main.go:330-351` 等）。
4. **可选：Tab 补全白名单**：`main.go:156-158` 的 `acceptCompleteFileCommands` 目前是
   ```go
   []string{"cd","cp","download","export","locate","ls","meta","mkdir","mv","rm","setastoken","share","transfer","tree","upload"}
   ```
   若 `reconcile` 要支持网盘路径补全，需把 `"reconcile"` 加进去。（注意 `upload` 在里面，但 upload 的补全逻辑会走到 `:211-275` 的分支。）
5. **`main.go` 顶部已导入的包**（`main.go:3-35`）：`baidupcs`、`internal/pcscommand`、`internal/pcsconfig`、`pcsutil/escaper`、`pcstable`、`pcsverbose`、`converter`、`pcstime` 等。新命令基本不需要新增 import。
6. `main()` 结构：`main.go:93` 起，`app := cli.NewApp()` `:96`，全局 `-verbose` flag `:118-125`，`app.Action`（交互式 liner 模式）`:126-327`，`app.Commands = []cli.Command{...}` `:328-2169`，`app.Run(os.Args)` 在末尾。`init()` `:79-91` 调 `pcsutil.ChWorkDir()` + `pcsconfig.Config.Init()`。

---

## 3. 配置 / 认证（`internal/pcsconfig`）

### 3.1 配置目录与环境变量

```go
// internal/pcsconfig/pcsconfig.go:18-23
const EnvConfigDir = "BAIDUPCS_GO_CONFIG_DIR"
const ConfigName   = "pcs_config.json"

// internal/pcsconfig/pcsconfig.go:270-313
func GetConfigDir() string
```
解析顺序（`:270-313`）：
1. 环境变量 `BAIDUPCS_GO_CONFIG_DIR`；**若为绝对路径直接返回**；否则相对 `pcsutil.ExecutablePathJoin(dir)`（= 可执行文件所在目录 + dir）（`:272-279`）。
2. 旧版兼容：若"程序所在目录/`pcs_config.json`"存在 → 用程序目录（`:283-287`）。
3. Windows：`%APPDATA%\BaiduPCS-Go`（`:290-296`；`APPDATA` 未设则回退程序目录）。
4. 其他：`$HOME/.config/BaiduPCS-Go`，且会 `os.MkdirAll(configDir, 0700)` 试探可写，失败回退程序目录（`:297-312`）。

```go
// internal/pcsconfig/pcsconfig.go:25-31
var (
    pcsConfigVerbose = pcsverbose.New("PCSCONFIG")
    configFilePath   = filepath.Join(GetConfigDir(), ConfigName)
    Config = NewConfig(configFilePath)   // ← 包级单例，测试必须重置
)
```
`configFilePath` 在**包初始化时求值一次**，所以运行中改 `BAIDUPCS_GO_CONFIG_DIR` 不生效。变通：直接给 `pcsconfig.NewConfig(newPath)` 赋给 `Config` 变量（`NewConfig` 是导出的，`pcsconfig.go:72`）。

### 3.2 登录态存储与获取已认证客户端

```go
// internal/pcsconfig/pcsconfig.go:34-69
type PCSConfig struct {
    BaiduActiveUID uint64        `json:"baidu_active_uid"`
    BaiduUserList  BaiduUserList `json:"baidu_user_list"`
    AppID int `json:"appid"`
    CacheSize, MaxParallel, MaxUploadParallel, MaxDownloadLoad, MaxUploadLoad int
    MaxDownloadRate, MaxUploadRate int64
    UserAgent, PCSUA, PCSAddr, PanUA, SaveDir string
    EnableHTTPS, FixPCSAddr bool
    ForceLogin, Proxy, ProxyHostnames, LocalAddrs string
    NoCheck, IgnoreIllegal bool
    UPolicy string `json:"u_policy"`      // ← 上传重名策略默认值来源
    // 私有: configFilePath, configFile, fileMu, activeUser *Baidu, pcs *baidupcs.BaiduPCS
}
```
```go
// internal/pcsconfig/baidu.go:32-47
type Baidu struct {
    BaiduBase                     // UID uint64, Name string
    Sex string; Age float64
    BDUSS, PTOKEN, STOKEN, BAIDUID, SBOXTKN, COOKIES string  // ← 全是机密
    AccessToken string
    Workdir string `json:"workdir"`   // 工作目录，cd 命令改它
}
// internal/pcsconfig/baidu.go:50
func (baidu *Baidu) BaiduPCS() *baidupcs.BaiduPCS
// internal/pcsconfig/baidu.go:80
func (baidu *Baidu) PathJoin(p string) string   // p 绝对则原样返回，否则 path.Join(Workdir, p)
// internal/pcsconfig/baidu.go:70
func (baidu *Baidu) GetSavePath(pcspath string) string  // 下载用，reconcile 不用
```
```go
// internal/pcsconfig/export.go:15
func (c *PCSConfig) ActiveUser() *Baidu             // activeUser==nil 时返回 &Baidu{}（空对象，不报错！）
// internal/pcsconfig/export.go:23
func (c *PCSConfig) ActiveUserBaiduPCS() *baidupcs.BaiduPCS  // pcs==nil 时惰性 c.ActiveUser().BaiduPCS()
// internal/pcsconfig/export.go:38  HTTPClient()    → UserAgent 客户端
// internal/pcsconfig/export.go:43  PCSHTTPClient() → PCSUA 客户端（上传用）
// internal/pcsconfig/export.go:48  PanHTTPClient() → PanUA 客户端
// internal/pcsconfig/export.go:53  NumLogins() int
```
- 认证信息装配：`BaiduPCS()`（`baidu.go:50-66`）→ `baidupcs.NewPCS(Config.AppID, baidu.BDUSS)` → `SetStoken` / `SetSboxtkn` / 若 `COOKIES` 含 `STOKEN=` 且 `STOKEN==""` 则改用 `NewPCSWithCookieStr` → `SetHTTPS/SetPCSUserAgent/SetPanUserAgent/SetUID/SetaccessToken`。
- `PCSConfig.init()`（`pcsconfig.go:137-173`）：`InitDefaultConfig()` → `loadConfigFromFile()` → 若 `activeUser.UID == BaiduActiveUID` 则**提前返回**（所以 `Reload()` 不会重建客户端）→ 否则 `GetBaiduUser` + `c.pcs = c.activeUser.BaiduPCS()` + `c.pcs.SetPCSAddr(c.PCSAddr)` → 并设置全局 `requester.UserAgent` / 全局代理 / 网卡地址。
- **未登录时的行为**：`ActiveUser()` 返回零值 `&Baidu{}`（`BDUSS==""`），`ActiveUserBaiduPCS()` 会返回一个**带空 BDUSS 的客户端**——即"未认证客户端"路径存在，但**没有任何显式守卫/错误**。`pcsconfig.ErrNotLogin`（`errors.go:9`）在整个仓库中**未被引用**。`reconcile` 必须自己检查 `GetActiveUser().Name == "" || GetActiveUser().BDUSS == ""` 并报错退出。

### 3.3 `pcscommand.go` 与 `runner.go` 内容（完整）

```go
// internal/pcscommand/pcscommand.go  (全文 22 行)
package pcscommand
var pcsCommandVerbose = pcsverbose.New("PCSCOMMAND")          // :11
func GetActiveUser() *pcsconfig.Baidu { return pcsconfig.Config.ActiveUser() }              // :15
func GetBaiduPCS() *baidupcs.BaiduPCS { return pcsconfig.Config.ActiveUserBaiduPCS() }      // :20
```
```go
// internal/pcscommand/runner.go  (全文 21 行)
var DefaultRunner = Runner{Output: os.Stdout}                 // :10
type Runner struct { Output io.Writer; IsBackground bool }    // :17-20
```
**注意：`DefaultRunner` 在仓库中没有任何使用点**（全仓搜索仅 `runner.go:9` 一处）—— 它是历史遗留/预留。想给 `reconcile` 做可测试输出，**可以**用它，但不要以为已有基础设施。

### 3.4 全局可变状态（测试必须处理）

| 全局变量 | 位置 | 影响 |
|---|---|---|
| `pcsconfig.Config` | `pcsconfig.go:30` | 整个配置 + `activeUser` + `pcs` 客户端；可被 `NewConfig(path)` 替换 |
| `pcsconfig.configFilePath` | `pcsconfig.go:27` | 包初始化时定死（私有） |
| `requester.UserAgent` | `pcsconfig.go:164` 设置 | 全局 UA |
| 全局代理 / 代理规则 / 本地网卡 | `pcsconfig.go:166-170` | `requester.SetGlobalProxy` / `SetProxyHostnameRules` / `SetLocalTCPAddrList` |
| `pcsverbose.IsVerbose` / `Outputs` | `pcsverbose/pcsverbose.go:17,20` | 调试输出开关与目标 writer（测试可替换 `Outputs` 捕获日志） |
| `pcsupload.uploadClient` + `uploadClientOnce` | `pcsupload/upload.go:29-30` | **Once 不可重置**，跨测试串 cookie |
| `pcsupload.uploadStopping` | `pcsupload/active.go:13` | 一旦 `GracefulStopActiveUploaders` 置 1，永不复位 |
| `baidupcs.DefaultOrderOptions` | `file_directory.go:101` | 可变指针 |
| `cachemap.GlobalCacheOpMap` | `expires/cachemap/cachemap.go:9` | 包级缓存池（虽然 `BaiduPCS.cacheOpMap` 是值拷贝，见 §5） |
| `taskframework` 的 executor 状态 | `pcsutil/taskframework/executor.go` | 每实例，无需重置 |

---

## 4. 路径处理与转义

### 4.1 常量与清洗

```go
// baidupcs/baidupcs.go:119-120
PathSeparator = "/"
// baidupcs/extends.go:20-23
ShellPatternCharacters = "*?[]"
```
- `pcsutil.ConvertToUnixPathSeparator(p string) string`（`pcsutil/file.go:120-123`）：`strings.Replace(p, "\\", "/", -1)`。**Windows 路径必须过它**。
- `pcsutil.ChPathLegal(p string) bool`（`pcsutil/file.go:125-135`）：非法字符集 `< > | " * ? , \`（Windows 额外含 `:`）。返回 false 时 upload 命令直接报错中止。
- `pcsutil.WalkDir(dirPth, suffix string) (files []string, err error)`（`pcsutil/file.go:83-118`）：
  - **跳过目录本身、跳过大小为 0 的文件**（`:92-98`）；
  - 支持软链接（断链跳过）（`:99-108`）；
  - suffix 大小写不敏感过滤（传 `""` 表示全部，`:110`）；
  - 结果为 `path.Clean(filename)`（**注意用 `path` 不是 `filepath`**）。
- `pcsutil.Executable()` / `ExecutablePath()` / `ExecutablePathJoin(sub)`（`:47/72/77`）。
- `pcsutil.TrimPathPrefix(path, prefixPath string) string`（`pcsutil/pcsutil.go:19-24`）。
- `baidu.PathJoin(p string) string`（`pcsconfig/baidu.go:80-85`）—— 命令层统一入口；`path.IsAbs(p)` 为真时**完全忽略 Workdir**。

### 4.2 通配符匹配（`ls` / `rm` / `mv` / `cp` 等）

```go
// baidupcs/extends.go:358
func (pcs *BaiduPCS) MatchPathByShellPattern(pattern string) (pcspaths []string, pcsError pcserror.Error)
// baidupcs/extends.go:331 (私有递归)
func (pcs *BaiduPCS) recurseMatchPathByShellPattern(index int, patternSlice *[]string, ps *[]string, pcspaths *[]string)
```
- 实现要点：`pattern` **必须是绝对路径**，否则返回 `ErrMatchPathByShellPatternNotAbsPath`（`extends.go:362-366`）；先 `path.Clean(pattern)`，再用 `escaper.Escape(..., []rune{'['})` 转义中括号（`:362`），按 `/` 切分后逐段递归；含通配符的段用 `path.Match` 匹配 `fds[k].Filename`（`:349`）；底层调 `FilesDirectoriesList`，**出错时 `panic(pcsError)`**，由 `MatchPathByShellPattern` 的 `defer recover()` 转回错误返回（`:343-346`, `:369-374`）。
- 命令层封装（`internal/pcscommand/util.go`）：
  ```go
  // :45
  func matchPathByShellPatternOnce(pattern *string) error      // 0 命中 → ErrShellPatternNoHit；>1 → ErrShellPatternMultiRes；1 → 就地改写 *pattern
  // :62
  func matchPathByShellPattern(patterns ...string) (pcspaths []string, err error)
  // :32
  func RunTestShellPattern(pattern string)                     // match 子命令用
  // :14-18
  var ErrShellPatternMultiRes = errors.New("多条通配符匹配结果")
  var ErrShellPatternNoHit    = errors.New("未匹配到路径, 请检测通配符")
  ```
- **哪些命令走通配符**（对 `reconcile` 很重要）：
  - 走：`RunLs`（`ls_search.go:34`）、`RunSearch`（`:58`）、`RunChangeDirectory`（`cd.go:12`）、`RunRemove`（`rm_mkdir.go:12`）、`RunGetMeta`（`meta.go:9`）、`RunFixMD5`（`fixmd5.go:10`）、`runCpMvOp` 的 `from`（`cp_mv.go:30`）+ `to` 的**条件**匹配（`:38-50`）、`RunUpload` 的 `savePath`（`upload.go:78`）、`RunTree` 仅 depth 0（`tree.go:28`）。
  - **不走**：`RunMkdir`（`rm_mkdir.go:40-42` 直接 `PathJoin`）、`RunDownload`（`download.go` 里 `paths` 直接交给 `FilesDirectoriesRecurseList`）。
- **`reconcile` 的建议**：远端根目录用 `matchPathByShellPatternOnce`（拒绝通配符歧义），**但注意**：`ls` 对不存在的路径也会先命中 `ErrShellPatternNoHit`。若要自己处理"目录不存在"，直接调 `FilesDirectoriesMeta` 判断更清晰。

### 4.3 中文 / 空格 / 括号

- **中文**：全链路按 UTF-8 字符串传递；`path`/`path.Match`/`escaper` 都是 `rune` 感知（`escaper.go:21` `rs := []rune(s)`）。**没有额外编码处理**，中文无需转义。
- **空格与引号**：只有**交互式 liner 模式**的 Tab 补全会转义：`main.go:193-209`
  ```go
  runeFunc = unicode.IsSpace
  pcsRuneFunc = func(r rune) bool { switch r { case '\'', '"': return true }; return unicode.IsSpace(r) }
  escaper.EscapeStringsByRuneFunc(lineArgs[:numArgs-1], runeFunc)   // :206
  escaper.EscapeByRuneFunc(path.Join(targetPath, file.Filename), pcsRuneFunc)  // :254, :261, :265
  ```
  **命令行模式（`os.Args`）下空格由 OS/shell 的 argv 分隔处理，cao 内部不再二次转义。**
- **括号 `()` / `[]`**：`[` `]` 是通配符字符（`ShellPatternCharacters = "*?[]"`），所以**远端文件名里含 `[` `]` 时**，`MatchPathByShellPattern` 依赖 `escaper.Escape(pattern, []rune{'['})` 只转义 `[`（`extends.go:362`）；`path.Match` 对 `[]` 是字符类。`(` `)` **不是**通配符，原样处理。
  - 陷阱：`cp_mv.go:38` 用 `strings.ContainsAny(to, baidupcs.ShellPatternCharacters)` 判断目标是否要匹配 → 目标名里含 `[` 会被误当通配符。`reconcile` 若构造远端路径**不要复用这个判断**。
- **是否经过 shell —— 明确回答：正常数据路径不经过 shell。**
  - 全仓 `os/exec` 只有两处：`main.go:7` import + `main.go:339` 的 `run` 子命令（`exec.Command(c.Args().First(), c.Args().Tail()...)`，用户显式执行系统命令，**argv 直传不经 shell**）；以及 `cmd/AndroidNDKBuild/main.go:9,108`（构建工具）。
  - **没有 `sh -c` / `cmd /c`**，没有 `os/exec` 参与的路径拼接。所以中文/空格/括号不会造成命令注入 —— 但 `reconcile` 也不应引入。

---

## 5. 缓存（`baidupcs/cache.go` 与 `CacheFilesDirectoriesList`）

```go
// baidupcs/cache.go:22
func (pcs *BaiduPCS) CacheFilesDirectoriesList(path string, options *OrderOptions) (fdl FileDirectoryList, pcsError pcserror.Error)
// baidupcs/cache.go:37
func (pcs *BaiduPCS) CacheUK() (uk int64, pcsError pcserror.Error)
// baidupcs/cache.go:10 (私有)
func (pcs *BaiduPCS) deleteCache(dirs []string)
```
- **读缓存**：`CacheFilesDirectoriesList` 走 `pcs.cacheOpMap.CacheOperation(OperationFilesDirectoriesList, key, opFunc)`，TTL **`1 * time.Minute`**（`:28`）。`opFunc` 内部调用 `FilesDirectoriesList`（`:24`，**无缓存版本**）。
- **写 key vs 删 key —— 已知不一致（重要坑）**：
  ```go
  // cache.go:13  deleteCache 用的 key
  key := v + "_" + defaultOrderOptionsStr
  // cache.go:23  CacheFilesDirectoriesList 用的 key
  path + "_" + string(options.By) + string(options.Order)
  ```
  `defaultOrderOptionsStr = fmt.Sprint(DefaultOrderOptions)`（`file_directory.go:106`）—— 那是 `&{name asc}` 之类的 `fmt` 输出，**永远不等于** `"nameasc"`。所以 `deleteCache` 只会在 `defaultOrderOptionsStr` 恰好等于某个 `By+Order` 时才命中，**实践中几乎打不中**。
- **这意味着**：调用 `CacheFilesDirectoriesList` 的代码（只有 `main.go:234` Tab 补全 和 `internal/pcsfunctions/pcsupload/upload_task_unit.go:151` 秒传前检查）可能拿到**最长 1 分钟前的过期目录**。
- **对 `reconcile` 的影响**：
  - 只要**不调用 `CacheFilesDirectoriesList`**，改用 `FilesDirectoriesList`（或 `FilesDirectoriesRecurseList`），就**完全绕过缓存**，不会拿到陈旧结果。
  - **但要注意反向风险**：`RapidUpload`/`FakeRapidUpload`/`UploadCreateSuperFile`/`Remove`/`Mkdir`/`Copy`/`Move` 成功后会调 `deleteCache`（`upload.go:109,121,221`；`rm_mkdir.go:23,42`；`cp_mv_rename.go:40`）。由于 key 不匹配，**这些失效基本无效**。所以如果一个 `reconcile` 进程内先上传再列目录，若用了缓存接口会看到旧数据；用 `FilesDirectoriesList` 则无此问题。
- 缓存实现细节：
  - `cachemap.CacheOpMap.CacheOperation(op, key, opFunc)`（`expires/cachemap/utils.go:12-30`）：`LockKey`（每 key 一把 `sync.Mutex`）→ `Load` → 未命中则执行 `opFunc` 并 `Store`。
  - `cacheUnit.Load`（`cacheunit.go:30-41`）会自动删除过期项；`Store` 拒绝已过期数据（`:64-69`）。
  - `expires.NewDataExpires(data, dur)`（`expires/dataexpires.go:19`）、`expires.NewExpires(dur)`（`expires/expires.go:31`，用 `//go:linkname` strip monotonic clock）。
  - `CacheUK` TTL 24 小时，key 是 `pcs.GetBDUSS()`（`cache.go:38`）—— **把 BDUSS 当缓存 key**，注意别把这个 key 打进日志。
  - **`BaiduPCS.cacheOpMap` 是值类型 `cachemap.CacheOpMap`（内含 `sync.Map`）**，`baidupcs.go:151`；`CopyPCS()` 会**拷贝整个 `cacheOpMap`**（`baidupcs.go:383`）——`sync.Map` 复制后不共享，但复制本身在 `go vet` 下是可疑写法。`reconcile` 用 `CopyPCS()` 得到的客户端**缓存不共享**，这反而有利于"每次新鲜读取"。
  - `cachemap.GlobalCacheOpMap`（`cachemap.go:9`）在仓库中**无任何使用点**。

---

## 6. 测试约定 / 构建

### 6.1 现有测试的写法

全部测试文件（22 个）：
`baidupcs/expires/cachemap/cachemap_test.go`、`baidupcs/netdisksign/{locatedownloadsign,sign2}_test.go`、`internal/pcscommand/upload_test.go`、`internal/pcsfunctions/pcsupload/upload_task_unit_test.go`、`pcsliner/args/args_test.go`、`pcsutil/file_test.go`、`pcsutil/cachepool/malloc_test.go`、`pcsutil/checksum/checksum_test.go`、`pcsutil/converter/{converter,short_display,size}_test.go`、`pcsutil/escaper/escaper_test.go`、`pcsutil/getip/getip_test.go`、`pcsutil/taskframework/taskframework_test.go`、`pcsutil/waitgroup/wait_group_test.go`、`requester/util_test.go`、`requester/downloader/{download,range}_test.go`、`requester/rio/multi_test.go`、`requester/rio/speeds/ratelimit_test.go`、`requester/uploader/block_test.go`。

两种风格：
1. **直接测包内未导出函数**（同 package 测试）：
   ```go
   // internal/pcscommand/upload_test.go  (全文 20 行)
   package pcscommand
   func TestUploadFailureError(t *testing.T) { ... uploadFailureError(0) ... }
   ```
2. **构造最小 struct + taskframework 壳，绕开网络**：
   ```go
   // internal/pcsfunctions/pcsupload/upload_task_unit_test.go:13-16
   func attachTaskInfo(unit *UploadTaskUnit) {
       executor := taskframework.NewTaskExecutor()
       executor.Append(unit, 0)        // 只是为了给 unit 注入 taskInfo，不 Execute
   }
   // :18  TestRunReportsOversizedFileAsFailure   → Length = baidupcs.MaxUploadSize + 1
   // :36  TestRunReportsUnreadableFileAsFailure → checksum.NewLocalFileChecksum(filepath.Join(t.TempDir(), "missing.mp4"), int(baidupcs.SliceMD5Size))
   // :52  TestJustGoonReturnsPreparedFailure     → 直接测 runPreparedStep()
   ```
   **这是 `reconcile` 测试可直接照抄的模式**：把"决策/比较/计划生成"抽成纯函数（不碰网络），用 `t.TempDir()` 造本地树，断言计划结果。

### 6.2 有没有 remote API 的 mock / fake / 接口？

**明确回答：没有。** 全仓 `interface {` 共 27 处（`grep "interface \{"`），全部列出如下，**没有一个是远程文件系统抽象**：

| 接口 | 位置 | 用途 |
|---|---|---|
| `pcserror.Error` | `baidupcs/pcserror/pcserror.go:14` | 错误类型（唯一与 baidupcs 相关的接口） |
| `expires.Expires` / `expires.DataExpires` | `expires/expires.go:10`, `dataexpires.go:8` | 缓存过期 |
| `cachemap.CacheUnit` | `expires/cachemap/cacheunit.go:9` | 缓存单元 |
| `TaskUnit` | `pcsutil/taskframework/task_unit.go:6` | 任务单元 |
| `uploader.MultiUpload` | `requester/uploader/multiuploader.go:17` | **最接近"可替换传输层"的接口** |
| `uploader.SplitUnit` / `Status` / `Readed64` | `requester/uploader/{block,status,readed}.go` | 分片 |
| `rio.{Lener,Lener64,ReaderLen,ReaderLen64,ReaderAtLen64,WriterLen64,WriteCloserAt,WriteCloserLen64At}` | `requester/rio/rio.go:10-50` | IO 抽象 |
| `requester.{ContentTyper,ContentLengther}` | `requester/requester.go:18,23` | |
| `transfer.{DownloadStatuser,DownloadInstanceInfoExporter}` | `requester/transfer/*.go` | |
| `downloader.{WorkerStatuser,Fder,Writer}` | `requester/downloader/*.go` | |
| `checksum.ChecksumWriter` | `pcsutil/checksum/checksum_write.go:9` | |
| `cachepool.Cache` | `pcsutil/cachepool/cachepool.go:13` | |
| `panhome.SignRes` | `baidupcs/internal/panhome/sign.go:11` | 签名 |

**没有 `*_test.go` 里的 mock/fake/stub 类型**（没有任何 `type mockXxx` / `fakeXxx`）。也没有 `httptest` 服务器测试。

→ 结论：如果 `reconcile` 想要可测试，**必须自己定义** `RemoteFS` 这类接口，并让 `*baidupcs.BaiduPCS` 满足它（方法集已经齐了：`FilesDirectoriesList` / `FilesDirectoriesMeta` / `Mkdir` / `Remove` / `Move` / `Copy` / `UploadTmpFile` / `UploadCreateSuperFile` …），再写一个内存 fake。**项目里没有任何现成对齐物可以复用。**

### 6.3 构建命令

`go.mod`：
- `module github.com/qjfoidnh/BaiduPCS-Go`、`go 1.23`。
- 关键依赖：`urfave/cli v1.22.5`（**v1**，不是 v2）、`json-iterator/go v1.1.12`、`olekukonko/tablewriter v0.0.4`、`tidwall/gjson v1.18.0`、`qjfoidnh/baidu-tools v1.2.0`、`rs/dnscache`。

`build.sh`（`:22-43` 的 `Build()`）：
```sh
export GOOS=$2 GOARCH=$3 GO386=sse2 CGO_ENABLED=0 GOARM=$4
$go build -ldflags "-X main.Version=$version -s -w" -o "$output/$1/$name.exe"
```
Windows 目标行（`:110-112`）：
```sh
Build $name-$version"-windows-x86"   windows 386
Build $name-$version"-windows-x64"   windows amd64
Build $name-$version"-windows-arm64" windows arm64
```
`RicePack` 已禁用（`build.sh:87-90` 直接 `return`，注释"已取消web功能"）。

README（`:293-308`）：
```
# Linux/Darwin 例子: 编译 Windows 下的 64 位程序
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build

# Windows 例子: 编译 Linux 下的 32 位程序
set GOOS=linux
set GOARCH=386
set CGO_ENABLED=0
go build
```
→ **Windows 上本地构建/测试的最简命令**：
```powershell
go build ./...            # 编译检查
go test ./internal/pcscommand/... ./internal/pcsfunctions/pcsupload/... ./baidupcs/...
go vet ./internal/pcscommand/...
```
（`resource_windows_*.syso` + `goversioninfo -platform-specific` 只在正式打包时需要；普通 `go build` 会因 `.syso` 自动链接资源而正常通过。）

---

## 7. 日志 / 输出 / 机密泄漏风险

### 7.1 `pcsverbose`

```go
// pcsverbose/pcsverbose.go:10-21
const EnvVerbose = "BAIDUPCS_GO_VERBOSE"
var IsVerbose = os.Getenv(EnvVerbose) == "1"
var Outputs = []io.Writer{os.Stderr}
// :29
func New(module string) *PCSVerbose
// :36/:41/:46/:51  方法
func (pv *PCSVerbose) Info(l string)
func (pv *PCSVerbose) Infof(format string, a ...interface{})
func (pv *PCSVerbose) Warn(l string)
func (pv *PCSVerbose) Warnf(format string, a ...interface{})
// :56/:70 包级
func Verbosef(format string, a ...interface{}) (n int, err error)
func Verboseln(a ...interface{}) (n int, err error)
```
- 全部输出到 `Outputs`（默认 `os.Stderr`），且**仅当 `IsVerbose`**（`Verbosef` 内 `if IsVerbose`，`:57`）。带时间前缀 `TimePrefix()`（`pcsverbose/utils.go`）。
- 全局开关由 `app.Flags` 的 `-verbose`/`BAIDUPCS_GO_VERBOSE` 绑定到 `pcsverbose.IsVerbose`（`main.go:118-125`）。
- 既有 module 名：`"BAIDUPCS"`（`baidupcs/baidupcs.go:124`）、`"PCSCOMMAND"`（`internal/pcscommand/pcscommand.go:11`）、`"PCSUPLOAD"`（`pcsupload/pcsupload.go:13`）、`"PCSCONFIG"`（`pcsconfig/pcsconfig.go:26`）、`"UPLOADER"`（`requester/uploader/uploader.go:42`）。
- 典型用法示例：
  - `baiduPCSVerbose.Infof("%s URL: %s\n", OperationFilesDirectoriesList, panURL)`（`prepare.go:194`）
  - `pcsUploadVerbose.Warnf("clear invalid file path: %s, err: %s\n", ...)`（`upload_database.go:221`）
  - `pcsCommandVerbose.Warnf("%s\n", pcsError)`（`internal/pcscommand/download.go:109`）
- **`reconcile` 建议**：在 `internal/pcscommand/reconcile.go` 直接用现有的 `pcsCommandVerbose`（同包可见），不要新建 module 名，除非要做独立过滤。

### 7.2 表格 / 输出 helper

```go
// pcstable/pcstable.go:15
func NewTable(wt io.Writer) PCSTable       // 预设: SetAutoWrapText(false), SetBorder(false), SetHeaderLine(false), SetColumnSeparator("")
type PCSTable struct { *tablewriter.Table }  // :10 内嵌，可直接用 tablewriter 的全部方法
```
既有报告用法（`reconcile` 可直接照抄的列样式）：
- `internal/pcscommand/ls_search.go:78-144` `renderTable(op, isTotal, path, files)`：**两种模式**（`Total=true` 列 `#/fs_id/app_id/文件大小/创建日期/修改日期/md5/路径`；否则 `#/文件大小/修改日期/路径`），目录名追加 `baidupcs.PathSeparator`（`:97,:122`），末行汇总 `files.Count()` + `files.TotalSize()`（`:115-116,:133-134`），超过 50 行再打印一次目录（`:139-141`）。
- `internal/pcscommand/upload.go:191-196`（失败列表）、`internal/pcscommand/recycle.go:27-38`、`internal/pcscommand/rm_mkdir.go:19-25`、`internal/pcscommand/cp_mv.go:117/121/127/131`（打印 `*CpMvListJSON` 的 `String()`）。
- 大小格式化 `pcsutil/converter.ConvertFileSize(size int64, floatNum int) string`；时间格式化 `pcsutil/pcstime.FormatTime(t int64) string`；`converter.ConvertFileSize` 与 `pcstime.FormatTime` 在 `ls_search.go:97,110,116,122,128` 都在用。

### 7.3 会打印机密的点（`reconcile` 必须避开）

| 风险点 | 位置 | 说明 |
|---|---|---|
| 登录帮助文本里的示例 cookie | `main.go:407`（`BaiduPCS-Go login -cookies="BDUSS=xxxxx; BAIDUID=yyyyyy; STOKEN=zzzzz; ...."`）、`README.md:405` | 只是文档字符串，但说明格式 |
| `GetBDUSS()` 被当作**缓存 key** | `baidupcs/cache.go:38` `pcs.cacheOpMap.CacheOperation(OperationGetUK, pcs.GetBDUSS(), ...)` | 若打印 cache key 就会泄露 BDUSS |
| `GetBDUSS()` 参与签名 | `baidupcs/prepare.go:209`（`netdisksign.NewLocateDownloadSign(pcs.uid, pcs.GetBDUSS())`）、`:455` | 只传参，不打印 |
| `Baidu.COOKIES` / `STOKEN` / `BDUSS` / `SBOXTKN` / `AccessToken` 字段 | `internal/pcsconfig/baidu.go:37-44` | **这些字段名一旦被 `fmt.Printf("%+v", baidu)` 或 JSON marshal 打印就会全泄露** |
| `PCSConfig.PrintTable()` | `internal/pcsconfig/export.go:63-92` | 已**刻意不含** BDUSS/STOKEN/cookies（只打 appid/UA/savedir 等）——**这是正确做法，照抄** |
| `BaiduUserList.String()` | `internal/pcsconfig/baidu.go:126-140` | 只输出 `#/uid/用户名/性别/age` —— **正确做法** |
| `FileDirectory.String()` 与 md5 列 | `baidupcs/file_directory.go:358` `"md5 (截图请打码)"`；`ls_search.go:93` `"md5(截图请打码)"` | 项目有意提醒 md5 敏感（因为 md5 可用于秒传/无文件复制） |
| `pcsverbose` 的 URL 调试日志 | `prepare.go:134,149,194,245,260,277,304` 等 | URL 里带 `app_id`，不含 cookie；`requester` 层不会打印 header |
| **不存在的风险** | — | 没有发现把 cookie/BDUSS 打进日志或表格的代码；`GetURLCookieString`（`pcsutil/pcsutil.go:46`）在仓库中**无使用点** |

**`reconcile` 的输出原则**：只打印路径、大小、时间、动作（`upload`/`skip`/`delete`/`mkdir`）；**不要**打印 `fd.MD5`（除 `--md5` 显式开关外），**绝不**打印 `Baidu` 结构体、`GetBDUSS()`、cookies，**绝不**把缓存 key 或完整请求 URL 之外的鉴别信息打进去。

---

## 8. 负面发现汇总（明确"不存在"的东西）

1. **不存在远程 API 接口抽象**（无 `RemoteFS`/`PCS` interface）；`*baidupcs.BaiduPCS` 是唯一实现，方法全是具体方法（§6.2）。想做单测必须自己引入接口 + fake。
2. **不存在任何 mock/fake/stub 类型**，也没有 `httptest` 服务器测试（§6.2）。
3. **不存在"本地单文件 → 精确远端文件路径"的上传 helper**；`RunUpload` 是"若干本地路径 → 远端目录"语义（§1.5d）。
4. **不存在 dry-run 基础设施**：全仓无 `DryRun` 字段/变量（无任何现有命令支持 dry-run）。
5. **不存在自动创建中间目录的逻辑**（§1.6）；`Mkdir` 不保证创建父目录。
6. **不存在"本地加密/哈希"通用 helper**（`pcsutil/crypto.go` 是文件整体 AES 加解密 + gzip，与 md5 对比无关）；只有 `pcsutil/checksum.LocalFileChecksum` 这个增量校验器（`checksum.go:44`，`NewLocalFileChecksum(localPath string, sliceSize int)` `:53`，`OpenPath()` `:68`，`Sum(checkSumFlag int)` `:197`，`CalculateChunkedSum(chunkSize int64)` `:241`，常量 `CHECKSUM_MD5|CHECKSUM_SLICE_MD5|CHECKSUM_CRC32` `:24-28`，`DefaultBufSize = 1MB` `:19`）。
7. **不存在 `no-split` 的实际实现**（`UploadTaskUnit.NoSplitFile` 定义了但 `Run()` 未使用，§1.5d）。
8. **不存在可重置的 `sync.Once` 上传客户端**（§3.4）。
9. **不存在命令级的输出注入点**（`DefaultRunner` 定义了但零使用，§3.3）。
10. **`pcsconfig.ErrNotLogin` 从未被引用**（§3.2），未登录不会自动报错。
11. **`cachemap.GlobalCacheOpMap` 从未被使用**（§5）。
12. **`Config.ActiveUser()` 永不返回 nil**，返回零值 `&Baidu{}`（§3.2），所以"未登录"检测必须靠字段判空。

---

## 9. `reconcile` 实现要点清单（基于以上事实）

1. 新文件 `internal/pcscommand/reconcile.go`：`func RunReconcile(localDir, remoteDir string, opt *ReconcileOptions) error`；`ReconcileOptions{DryRun bool; Policy string; Delete bool; OnlyHash bool; Parallel int; MaxRetry int}`。
2. 未登录守卫：`GetActiveUser()` 的 `Name`/`BDUSS` 为空 → 返回错误。
3. 远端根目录：`GetBaiduPCS().FilesDirectoriesMeta(activeUser.PathJoin(remoteDir))` 判存在/是否目录；**不要**用 `CacheFilesDirectoriesList`。
4. 远端树收集：`FilesDirectoriesList(dir, orderOptions)` 逐层递归（或 `FilesDirectoriesRecurseList` + 回调），**自建 map[相对路径]*FileDirectory**；只信 `Isdir`/`Size`/`Filename`/`Path`；`MD5` 仅在 `len(BlockList)==1` 时用。
5. 本地树收集：`pcsutil.WalkDir(localDir, "")`（注意跳过 0 字节文件！）+ `os.Lstat` 拿 size/mtime；Windows 先 `ConvertToUnixPathSeparator`。
6. 比较：优先 size；`--hash` 时本地算 md5（`checksum.NewLocalFileChecksum`）对比 `fd.BlockList[0]`。
7. 计划与执行分离：先生成 `[]action`，`DryRun` 时只渲染表格（`pcstable.NewTable(os.Stdout)`）并 return。
8. 建目录：按需 **逐级** `pcs.Mkdir(path.Dir(p))`，忽略"已存在"。
9. 上传：组装 `pcsupload.UploadTaskUnit{LocalFileChecksum, SavePath: <精确远端路径>, PCS: GetBaiduPCS().CopyPCS(), UploadingDatabase, Parallel, Policy: baidupcs.OverWritePolicy (或 -policy), UploadStatistic}` + `taskframework.TaskExecutor`。
10. 删除（可选）：`pcs.Remove(paths...)`（进回收站，可恢复）；`--dry-run` 不调。
11. 错误分类：`GetErrType()` + `GetRemoteErrCode()`，**先识别 `114514`/`1919810` 自定义码**（§1.8）。
12. `main.go`：在 `:1249` 之后插入命令块，`Before: reloadFn`，`Category: "百度网盘"`；可选把 `"reconcile"` 加进 `main.go:157` 的补全白名单。
13. 测试：照抄 `upload_task_unit_test.go` 的 `attachTaskInfo` 模式测纯函数；引入自定义 `RemoteFS` 接口 + 内存 fake（项目无现成物）。
