package reconcile

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
)

// RemovalMode selects how redundant cloud files leave the mirror.
//
// There is deliberately no permanent-delete mode. Every path that removes
// content keeps a recovery route: quarantine is an ordinary cloud folder whose
// contents can be moved back, and recycle relies on the provider's own
// retention (observed at ~35 days on a live account).
type RemovalMode string

const (
	// RemovalQuarantine moves redundant entries into a folder outside the
	// mirror root. It is the default because recovery is explicit and does not
	// depend on the provider's retention policy.
	RemovalQuarantine RemovalMode = "quarantine"
	// RemovalRecycle deletes into the cloud recycle bin.
	RemovalRecycle RemovalMode = "recycle"
)

// QuarantineDirName is the default quarantine folder, created at the cloud root
// so that it can never be scanned as part of a mirror whose root is deeper.
const QuarantineDirName = "/BackupReconcile_Quarantine"

// ApplyOptions configures execution.
type ApplyOptions struct {
	PlanOptions
	// RemovalMode defaults to quarantine.
	RemovalMode RemovalMode
	// QuarantineRoot is where removed entries are moved. Empty means
	// QuarantineDirName.
	QuarantineRoot string
	// ConfirmRemoval must be true for the removal phase to run. It exists so
	// that a caller can perform uploads without authorising deletions.
	ConfirmRemoval bool
	// RawRemove additionally requires this to be set when RemovalMode is
	// recycle, so switching to the less recoverable mode is a deliberate act.
	AcknowledgeRecycle bool
	// MaxRemovePercent and MaxRemoveBytes are the blast-radius thresholds. A
	// plan exceeding either is refused unless ConfirmRemoval is set AND
	// AcknowledgeBlast is set.
	MaxRemovePercent float64
	MaxRemoveBytes   int64
	// AcknowledgeBlast confirms a removal that exceeds the thresholds.
	AcknowledgeBlast bool
	// Now is the reference time for the editing check (test seam).
	Now time.Time
}

// ApplyResult is the outcome of one action.
type ApplyResult struct {
	Action PlannedAction
	Phase  string // "mkdir" | "upload" | "verify" | "remove"
	OK     bool
	Skipped bool
	Detail string
	// Error is the failure message when OK is false.
	Error string
}

// ApplyReport is the full record of an apply run. It is written to disk so an
// interrupted run can be reviewed and completed.
type ApplyReport struct {
	ApplyVersion string
	StartedAt    string
	FinishedAt   string
	LocalRoot    string
	RemoteRoot   string
	RemovalMode  string
	Quarantine   string
	// RunID identifies this run's quarantine subfolder.
	RunID string

	Mkdirs   []ApplyResult
	Uploads  []ApplyResult
	Verifies []ApplyResult
	Removes  []ApplyResult

	// UploadFailures counts failed uploads/replaces.
	UploadFailures int
	// RemovalSkipped reports that the removal phase did not run by design.
	RemovalSkipped bool
	RemovalReason  string
	// Aborted reports that execution stopped early.
	Aborted bool
	AbortReason string
	// RecoverySteps tells the operator how to undo this run.
	RecoverySteps []string
	Warnings      []string
}

// Succeeded reports whether every attempted action succeeded.
func (r *ApplyReport) Succeeded() bool {
	if r.Aborted || r.RemovalSkipped {
		return false
	}
	for _, set := range [][]ApplyResult{r.Mkdirs, r.Uploads, r.Removes} {
		for _, res := range set {
			if !res.OK && !res.Skipped {
				return false
			}
		}
	}
	return true
}

// Executor performs a plan. The Writer half of the cloud API lives here and
// nowhere else, so the planning code physically cannot mutate anything.
type Executor struct {
	// Writer is the mutating cloud surface.
	Writer Writer
	// LocalVerified re-checks a local file against its scan snapshot before it
	// is uploaded. Nil uses CheckLocalUnchanged.
	LocalVerified func(LocalEntry) error
	// Log receives progress lines.
	Log func(string)
	// Now is the clock (test seam).
	Now func() time.Time
}

// Writer is the mutating cloud surface needed by the executor.
type Writer interface {
	Mkdir(remotePath string) error
	ListDir(remotePath string) ([]RemoteEntry, error)
	Meta(remotePath string) (RemoteEntry, error)
	Upload(localPath, remotePath string, size int64) error
	Move(from, to string) error
	Remove(remotePaths ...string) error
}

func (e *Executor) logf(format string, a ...interface{}) {
	if e.Log != nil {
		e.Log(fmt.Sprintf(format, a...))
	}
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Apply executes a plan.
//
// The order is the safety property: directories, then uploads/replaces, then a
// verification pass over everything that was written, and only then removals.
// Any failure before the removal phase skips removal entirely, because a cloud
// copy is only redundant once its local counterpart is confirmed present.
func (e *Executor) Apply(plan *Plan, opt ApplyOptions) (*ApplyReport, error) {
	if e == nil || e.Writer == nil {
		return nil, ErrNoClient
	}
	if plan == nil {
		return nil, fmt.Errorf("%w: 缺少计划", ErrUsage)
	}
	if plan.LocalRoot == "" || plan.RemoteRoot == "" {
		return nil, fmt.Errorf("%w: 计划缺少本地或云端根目录", ErrUsage)
	}
	if opt.RemovalMode == "" {
		opt.RemovalMode = RemovalQuarantine
	}
	quarantine := opt.QuarantineRoot
	if quarantine == "" {
		quarantine = QuarantineDirName
	}
	quarantine = NormalizeRemoteRoot(quarantine)

	// The quarantine area must not live inside the mirror, or the next run
	// would see its contents as redundant cloud files and move them again.
	if IsSubPath(plan.RemoteRoot, quarantine) {
		return nil, fmt.Errorf("%w: 隔离区 %s 不能位于镜像根目录 %s 之内",
			ErrUsage, quarantine, plan.RemoteRoot)
	}

	runID := e.now().Format("20060102-150405")
	quarantineRun := quarantine + "/" + runID
	if err := assertInsideRoot(quarantine, quarantineRun); err != nil {
		return nil, err
	}

	rep := &ApplyReport{
		ApplyVersion: ApplyVersion,
		StartedAt:    e.now().Format(time.RFC3339),
		LocalRoot:    plan.LocalRoot,
		RemoteRoot:   plan.RemoteRoot,
		RemovalMode:  string(opt.RemovalMode),
		Quarantine:   quarantine,
		RunID:        runID,
	}

	// Refuse to act on a plan that was built from an incomplete picture.
	if !plan.ScanComplete || !plan.LocalScanComplete {
		rep.Aborted = true
		rep.AbortReason = "计划基于不完整的扫描, 拒绝执行"
		return rep, fmt.Errorf("%w: %s", ErrUsage, rep.AbortReason)
	}

	// --- Phase 1: directories ---
	for _, a := range plan.Mkdirs {
		if err := assertInsideRoot(plan.RemoteRoot, a.RemotePath); err != nil {
			rep.Aborted = true
			rep.AbortReason = err.Error()
			return rep, err
		}
		res := ApplyResult{Action: a, Phase: "mkdir"}
		if err := e.ensureDir(a.RemotePath); err != nil {
			res.Error = err.Error()
			rep.Mkdirs = append(rep.Mkdirs, res)
			rep.Aborted = true
			rep.AbortReason = "创建云端目录失败: " + a.RelPath
			return rep, nil
		}
		res.OK = true
		rep.Mkdirs = append(rep.Mkdirs, res)
		e.logf("MKDIR  %s", a.RemotePath)
	}

	// --- Phase 2: uploads and replaces ---
	for _, set := range [][]PlannedAction{plan.Uploads, plan.Replaces} {
		for _, a := range set {
			if err := assertInsideRoot(plan.RemoteRoot, a.RemotePath); err != nil {
				rep.Aborted = true
				rep.AbortReason = err.Error()
				return rep, err
			}
			res := e.uploadOne(plan, a, opt)
			rep.Uploads = append(rep.Uploads, res)
			if !res.OK {
				rep.UploadFailures++
			}
		}
	}
	if rep.UploadFailures > 0 {
		// A cloud copy is only provably redundant once its local counterpart is
		// up there. Stop before any removal.
		rep.RemovalSkipped = true
		rep.RemovalReason = fmt.Sprintf("上传/覆盖阶段有 %d 项失败, 按安全规则跳过全部清理", rep.UploadFailures)
		rep.RecoverySteps = append(rep.RecoverySteps,
			"修正上报失败的文件后重新运行 apply; 本次未做任何清理")
		rep.FinishedAt = e.now().Format(time.RFC3339)
		return rep, nil
	}

	// --- Phase 3: verify what was written ---
	if err := e.verifyUploads(plan, rep); err != nil {
		rep.Aborted = true
		rep.AbortReason = err.Error()
		rep.RemovalSkipped = true
		rep.RemovalReason = "写入后校验失败, 跳过全部清理"
		rep.FinishedAt = e.now().Format(time.RFC3339)
		return rep, nil
	}

	// --- Phase 4: removals ---
	if len(plan.Removes) == 0 {
		rep.FinishedAt = e.now().Format(time.RFC3339)
		return rep, nil
	}
	if !opt.ConfirmRemoval {
		rep.RemovalSkipped = true
		rep.RemovalReason = "未确认清理 (需要显式确认); 上传与覆盖已完成"
		rep.RecoverySteps = append(rep.RecoverySteps,
			"确认后重新运行并加上清理确认参数即可完成多余项清理")
		rep.FinishedAt = e.now().Format(time.RFC3339)
		return rep, nil
	}
	if opt.RemovalMode == RemovalRecycle && !opt.AcknowledgeRecycle {
		rep.RemovalSkipped = true
		rep.RemovalReason = "回收站模式需要额外确认 (隔离区模式可随时移回)"
		rep.FinishedAt = e.now().Format(time.RFC3339)
		return rep, nil
	}
	if reason := e.blastRadiusRefusal(plan, opt); reason != "" {
		rep.RemovalSkipped = true
		rep.RemovalReason = reason
		rep.FinishedAt = e.now().Format(time.RFC3339)
		return rep, nil
	}

	if err := e.removeAll(plan, opt, quarantineRun, rep); err != nil {
		rep.Aborted = true
		rep.AbortReason = err.Error()
	}
	rep.FinishedAt = e.now().Format(time.RFC3339)
	return rep, nil
}

// blastRadiusRefusal checks the configured thresholds.
func (e *Executor) blastRadiusRefusal(plan *Plan, opt ApplyOptions) string {
	if opt.AcknowledgeBlast {
		return ""
	}
	files := plan.RemoveFiles()
	bytes := plan.RemoveBytes()
	if opt.MaxRemovePercent > 0 && plan.CloudFiles > 0 {
		pct := float64(files) / float64(plan.CloudFiles) * 100
		if pct > opt.MaxRemovePercent {
			return fmt.Sprintf(
				"拟清理 %d 个文件, 占云端文件数 %.1f%%, 超过阈值 %.1f%%; 需要额外确认",
				files, pct, opt.MaxRemovePercent)
		}
	}
	if opt.MaxRemoveBytes > 0 && bytes > opt.MaxRemoveBytes {
		return fmt.Sprintf("拟清理 %s, 超过阈值 %s; 需要额外确认",
			converter.ConvertFileSize(bytes, 2), converter.ConvertFileSize(opt.MaxRemoveBytes, 2))
	}
	return ""
}

// ensureDir creates a remote directory, tolerating the one error that means
// "already there".
func (e *Executor) ensureDir(remotePath string) error {
	if meta, err := e.Writer.Meta(remotePath); err == nil && meta.IsDir {
		return nil
	}
	err := e.Writer.Mkdir(remotePath)
	if err == nil {
		return nil
	}
	// A concurrent creation, or a provider that reports "already exists", is
	// not a failure: re-check.
	if meta, metaErr := e.Writer.Meta(remotePath); metaErr == nil && meta.IsDir {
		return nil
	}
	return err
}

// uploadOne uploads or replaces a single file, re-verifying the local snapshot
// first so a file that changed since planning is never uploaded half-written.
func (e *Executor) uploadOne(plan *Plan, a PlannedAction, opt ApplyOptions) ApplyResult {
	res := ApplyResult{Action: a, Phase: "upload"}

	le, ok := e.localEntryFor(plan, a)
	if !ok {
		res.Error = "计划中的本地路径不在本次本地树中"
		return res
	}

	verify := e.LocalVerified
	if verify == nil {
		verify = CheckLocalUnchanged
	}
	if err := verify(le); err != nil {
		res.Skipped = true
		res.Detail = "本地文件已变化, 跳过: " + err.Error()
		e.logf("SKIP   %s (%s)", a.RelPath, res.Detail)
		return res
	}

	// Guard the one case upstream does not: a same-named cloud DIRECTORY at the
	// target path. Uploading onto it would replace a directory tree.
	if meta, err := e.Writer.Meta(a.RemotePath); err == nil && meta.IsDir {
		res.Error = "云端同路径是目录, 拒绝上传覆盖 (需人工处理)"
		e.logf("FAIL   %s (%s)", a.RelPath, res.Error)
		return res
	}

	if err := e.Writer.Upload(le.AbsPath, a.RemotePath, le.Size); err != nil {
		res.Error = err.Error()
		e.logf("FAIL   %s (%s)", a.RelPath, res.Error)
		return res
	}
	res.OK = true
	res.Detail = fmt.Sprintf("已上传 %s", converter.ConvertFileSize(le.Size, 2))
	e.logf("UP     %s", a.RelPath)
	return res
}

// localEntryFor reconstructs the local entry the plan was built from, using the
// size and mtime RECORDED IN THE PLAN rather than a fresh stat.
//
// Using the live values would defeat the purpose: CheckLocalUnchanged compares
// against these fields, so a freshly stat'ed file would always "match itself"
// and a file modified between planning and execution would be uploaded as if it
// were final.
func (e *Executor) localEntryFor(plan *Plan, a PlannedAction) (LocalEntry, bool) {
	if a.LocalPath == "" {
		return LocalEntry{}, false
	}
	if _, err := os.Stat(a.LocalPath); err != nil {
		return LocalEntry{}, false
	}
	return LocalEntry{
		RelPath: a.RelPath,
		AbsPath: a.LocalPath,
		Name:    path.Base(a.LocalPath),
		Size:    a.LocalSize,
		Mtime:   a.LocalMtime,
		IsDir:   false,
	}, true
}

// verifyUploads re-lists the cloud and confirms every uploaded file is present
// with the expected size. A silent upload failure would otherwise let the
// removal phase strip a cloud copy whose replacement never arrived.
func (e *Executor) verifyUploads(plan *Plan, rep *ApplyReport) error {
	want := make(map[string]int64)
	for _, res := range rep.Uploads {
		if res.OK {
			want[res.Action.RemotePath] = res.Action.Size
		}
	}
	if len(want) == 0 {
		return nil
	}
	for remotePath, size := range want {
		meta, err := e.Writer.Meta(remotePath)
		res := ApplyResult{Phase: "verify", Action: PlannedAction{RemotePath: remotePath}}
		switch {
		case err != nil && IsNotFound(err):
			res.Error = "上传后云端未找到该文件"
			rep.Verifies = append(rep.Verifies, res)
			return fmt.Errorf("写入后校验失败: %s 不存在", remotePath)
		case err != nil:
			res.Error = err.Error()
			rep.Verifies = append(rep.Verifies, res)
			return fmt.Errorf("写入后校验失败: %s: %w", remotePath, err)
		case meta.IsDir:
			res.Error = "上传后该路径是目录"
			rep.Verifies = append(rep.Verifies, res)
			return fmt.Errorf("写入后校验失败: %s 是目录", remotePath)
		case size > 0 && meta.Size != size:
			res.Error = fmt.Sprintf("大小不符 (期望 %d, 实际 %d)", size, meta.Size)
			rep.Verifies = append(rep.Verifies, res)
			return fmt.Errorf("写入后校验失败: %s %s", remotePath, res.Error)
		default:
			res.OK = true
			res.Detail = "云端已确认存在"
			rep.Verifies = append(rep.Verifies, res)
		}
	}
	return nil
}

// removeAll moves or recycles every redundant entry.
//
// Files are moved one by one so a failure is isolated and the recovery list is
// precise; directories are moved as units, which is safe because the planner
// only marks a directory when nothing beneath it exists locally.
func (e *Executor) removeAll(plan *Plan, opt ApplyOptions, quarantineRun string, rep *ApplyReport) error {
	var recycleFiles []string

	// Order: deepest first, so a directory is only moved once its redundant
	// children have been dealt with and nothing is left dangling.
	actions := append([]PlannedAction(nil), plan.Removes...)
	sort.Slice(actions, func(i, j int) bool {
		di, dj := strings.Count(actions[i].RelPath, "/"), strings.Count(actions[j].RelPath, "/")
		if di != dj {
			return di > dj
		}
		return actions[i].RelPath < actions[j].RelPath
	})

	for _, a := range actions {
		res := ApplyResult{Action: a, Phase: "remove"}
		if err := assertInsideRoot(plan.RemoteRoot, a.RemotePath); err != nil {
			res.Error = err.Error()
			rep.Removes = append(rep.Removes, res)
			return err
		}
		switch opt.RemovalMode {
		case RemovalRecycle:
			// Batch the files; directories are removed individually so a
			// partial failure is attributable.
			if !a.IsDir {
				recycleFiles = append(recycleFiles, a.RemotePath)
				res.OK = true
				res.Detail = "已加入回收站批次"
				rep.Removes = append(rep.Removes, res)
				continue
			}
			if err := e.Writer.Remove(a.RemotePath); err != nil {
				res.Error = err.Error()
				rep.Removes = append(rep.Removes, res)
				return nil
			}
			res.OK = true
			res.Detail = "已移入回收站 (可还原)"
			rep.Removes = append(rep.Removes, res)
			e.logf("RECYCLE %s", a.RelPath)
		default:
			target := quarantineRun + "/" + a.RelPath
			if a.IsDir {
				target = quarantineRun + "/" + a.RelPath
			}
			if err := e.ensureDir(quarantineRun); err != nil {
				res.Error = "无法创建隔离区: " + err.Error()
				rep.Removes = append(rep.Removes, res)
				return err
			}
			if err := e.ensureDir(ParentRemote(target)); err != nil {
				res.Error = "无法创建隔离区子目录: " + err.Error()
				rep.Removes = append(rep.Removes, res)
				return err
			}
			if err := e.Writer.Move(a.RemotePath, target); err != nil {
				res.Error = err.Error()
				rep.Removes = append(rep.Removes, res)
				// Never fall back to a permanent delete when a move fails.
				rep.Warnings = append(rep.Warnings, fmt.Sprintf(
					"移动 %s 到隔离区失败, 已停止清理 (不会改为永久删除)", a.RelPath))
				return nil
			}
			res.OK = true
			res.Detail = "已移入隔离区: " + target
			rep.Removes = append(rep.Removes, res)
			e.logf("QUAR   %s -> %s", a.RelPath, target)
		}
	}

	if len(recycleFiles) > 0 {
		err := e.Writer.Remove(recycleFiles...)
		res := ApplyResult{Phase: "remove", Action: PlannedAction{
			Kind: ActionRemove, RelPath: fmt.Sprintf("(%d 个文件)", len(recycleFiles)),
		}}
		if err != nil {
			res.Error = err.Error()
			rep.Removes = append(rep.Removes, res)
			return nil
		}
		res.OK = true
		res.Detail = "已移入回收站 (可还原)"
		rep.Removes = append(rep.Removes, res)
		e.logf("RECYCLE %d files", len(recycleFiles))
	}

	if opt.RemovalMode == RemovalQuarantine {
		rep.RecoverySteps = append(rep.RecoverySteps, fmt.Sprintf(
			"恢复方式: 在网盘中把 %s/ 里的文件移回原相对路径即可 (清单见本报告)", quarantineRun))
	} else {
		rep.RecoverySteps = append(rep.RecoverySteps,
			"恢复方式: 在网盘「回收站」中按路径还原 (保留约 35 天)")
	}
	return nil
}

// ParentRemote returns the parent of an absolute cloud path.
func ParentRemote(p string) string {
	p = NormalizeRemoteRoot(p)
	if p == "/" {
		return "/"
	}
	parent := path.Dir(p)
	if parent == "." || parent == "" {
		return "/"
	}
	return NormalizeRemoteRoot(parent)
}

// ApplyVersion identifies the executor implementation in saved reports.
const ApplyVersion = "0.1.0"
