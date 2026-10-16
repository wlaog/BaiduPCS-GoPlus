package reconcile

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
)

type RemovalMode string

const (
	RemovalRecycle    RemovalMode = "recycle"
	RemovalQuarantine RemovalMode = "quarantine"
)
const QuarantineDirName = "/BackupReconcile_Quarantine"

type ApplyOptions struct {
	PlanOptions
	RemovalMode                                          RemovalMode
	QuarantineRoot                                       string // legacy input: no quarantine directories are created
	ConfirmRemoval, AcknowledgeRecycle, AcknowledgeBlast bool
	MaxRemovePercent                                     float64
	MaxRemoveBytes                                       int64
	Now                                                  time.Time
}

type ApplyResult struct {
	Action      PlannedAction
	Phase       string
	OK, Skipped bool
	// TargetRemoved survives a failed rename so a retry knows the canonical
	// old version has already left the target path.
	TargetRemoved  bool
	DeferredSource bool
	verifiedTarget *RemoteEntry
	Detail, Error  string
}

type ApplyReport struct {
	ApplyVersion, StartedAt, FinishedAt, LocalRoot, RemoteRoot, RemovalMode string
	Renames, Removes                                                        []ApplyResult
	Failures, PendingCount, ConflictCount                                   int
	UnverifiedCount                                                         int
	RemovalSkipped                                                          bool
	RemovalReason                                                           string
	Aborted                                                                 bool
	AbortReason                                                             string
	RecoverySteps, Warnings                                                 []string
}

func (r *ApplyReport) Succeeded() bool {
	if r.Aborted || r.RemovalSkipped {
		return false
	}
	for _, set := range [][]ApplyResult{r.Renames, r.Removes} {
		for _, res := range set {
			if !res.OK {
				return false
			}
		}
	}
	return true
}

// Writer deliberately has no upload, copy or mkdir operation. Reconciliation
// cannot create content even accidentally; the official backup owns that work.
type Writer interface {
	Reader
	Move(from, to string) error
	Remove(remotePaths ...string) error
}

type Executor struct {
	Writer        Writer
	Progress      ProgressFunc
	Log           func(string)
	LocalVerified func(LocalEntry) error
	Now           func() time.Time
	Retry         RetryPolicy
	Sleep         func(time.Duration)
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
func (e *Executor) logf(f string, a ...interface{}) {
	if e.Log != nil {
		e.Log(fmt.Sprintf(f, a...))
	}
}

// Apply serializes cloud mutations. Errors are isolated to the affected item;
// duplicate cleanup depends only on its own canonical version, not other files.
func (e *Executor) Apply(plan *Plan, opt ApplyOptions) (rep *ApplyReport, err error) {
	if e == nil || e.Writer == nil {
		return nil, ErrNoClient
	}
	if plan == nil || plan.LocalRoot == "" || plan.RemoteRoot == "" {
		return nil, fmt.Errorf("%w: 缺少有效预览", ErrUsage)
	}
	if NormalizeRemoteRoot(plan.RemoteRoot) == "/" {
		return nil, fmt.Errorf("%w: 拒绝操作根目录", ErrUsage)
	}
	if len(plan.Uploads)+len(plan.Replaces)+len(plan.Mkdirs) > 0 {
		return nil, fmt.Errorf("旧版上传计划不可执行，请重新预览；当前仅支持删除和重命名")
	}
	if opt.RemovalMode != "" && opt.RemovalMode != RemovalRecycle {
		return nil, fmt.Errorf("当前仅使用网盘回收站，不创建隔离目录")
	}
	rep = &ApplyReport{ApplyVersion: ApplyVersion, StartedAt: e.now().Format(time.RFC3339), LocalRoot: plan.LocalRoot, RemoteRoot: plan.RemoteRoot, RemovalMode: string(RemovalRecycle), PendingCount: len(plan.Pending), ConflictCount: len(plan.Conflicts)}
	rep.UnverifiedCount = len(plan.ScanIssues)
	defer func() { rep.FinishedAt = e.now().Format(time.RFC3339) }()
	if !plan.CanApply() {
		rep.Aborted = true
		rep.AbortReason = "扫描不完整，拒绝执行"
		return rep, fmt.Errorf("%w: %s", ErrUsage, rep.AbortReason)
	}
	if err := checkLocalRoot(plan); err != nil {
		return rep, err
	}
	if plan.LocalFiles == 0 && plan.LocalDirs == 0 {
		return rep, fmt.Errorf("本地目录为空，拒绝批量清理")
	}
	if !opt.ConfirmRemoval || !opt.AcknowledgeRecycle {
		rep.RemovalSkipped = true
		rep.RemovalReason = "请确认本次重命名及回收站清理"
		return rep, nil
	}
	if reason := e.blastRadiusRefusal(plan, opt); reason != "" {
		rep.RemovalSkipped = true
		rep.RemovalReason = reason
		return rep, nil
	}
	rep.RecoverySteps = []string{"被清理的文件及重命名前的同名旧版可在网盘回收站恢复；重命名来源与目标见本报告，恢复期限以网盘为准"}
	for _, set := range [][]PlannedAction{plan.Renames, plan.Removes} {
		for _, a := range set {
			if err := assertInsideRoot(plan.RemoteRoot, a.RemotePath); err != nil {
				return rep, err
			}
			if a.Kind == ActionRename {
				if err := assertInsideRoot(plan.RemoteRoot, a.SourcePath); err != nil {
					return rep, err
				}
				if ParentRemote(a.SourcePath) != ParentRemote(a.RemotePath) {
					return rep, fmt.Errorf("重命名来源与目标必须在同一目录")
				}
			}
		}
	}
	total := int64(plan.Changes())
	var done int64
	failedVersions := make(map[string]bool)
	for _, a := range plan.Renames {
		e.Progress.emit(Progress{Phase: "rename", Current: a.RelPath, Done: done, Total: total})
		res := e.executeItem(plan, a, "rename")
		rep.Renames = append(rep.Renames, res)
		if !res.OK {
			rep.Failures++
			failedVersions[a.RemotePath] = true
		}
		done++
		e.Progress.emit(Progress{Phase: "rename", Current: a.RelPath, Done: done, Total: total})
	}
	for _, a := range plan.Removes {
		e.Progress.emit(Progress{Phase: "remove", Current: a.RelPath, Done: done, Total: total})
		var res ApplyResult
		if failedVersions[a.Requires] {
			res = ApplyResult{Action: a, Phase: "remove", Skipped: true, Error: "对应版本重命名未完成，保留该历史副本；其他独立项继续执行"}
			e.logf("跳过 %s: %s", a.RelPath, res.Error)
		} else {
			res = e.executeItem(plan, a, "remove")
		}
		rep.Removes = append(rep.Removes, res)
		if !res.OK {
			rep.Failures++
		}
		done++
		e.Progress.emit(Progress{Phase: "remove", Current: a.RelPath, Done: done, Total: total})
	}
	return rep, nil
}

// nonRetryable marks changed preconditions. A retry must never delete a newly
// uploaded file simply because it now occupies a path in the old preview.
type changedState struct{ message string }

func (e *changedState) Error() string          { return e.message }
func changed(f string, a ...interface{}) error { return &changedState{fmt.Sprintf(f, a...)} }
func asyncPending() error {
	return &RemoteError{ErrType: pcserror.ErrTypeRemoteError, Code: 31171, Msg: "云端操作尚未确认完成，稍后重试"}
}

func (e *Executor) executeItem(plan *Plan, a PlannedAction, phase string) (res ApplyResult) {
	res = ApplyResult{Action: a, Phase: phase}
	if plan.actionUnverified(a) {
		res.Skipped, res.Error = true, "此项涉及未核实的扫描范围，已保护；其他独立项继续"
		return res
	}
	defer func() {
		if recover() != nil {
			res.OK = false
			res.Error = "该项发生异常，已跳过；可单独重试"
			e.logf("失败 %s: %s", a.RelPath, res.Error)
		}
	}()
	sleep := e.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	attempt := 0
	err, attempts, _ := withRetry(e.Retry, sleep, nil, func() error {
		attempt++
		if attempt > 1 {
			e.logf("重试第 %d 次 %s（先复核状态）", attempt, a.RelPath)
		}
		if phase == "rename" {
			return e.renameOne(plan, &res)
		}
		return e.removeOne(plan, &res)
	})
	if err != nil {
		res.Error = err.Error()
		var stale *changedState
		res.Skipped = errors.As(err, &stale)
		e.logf("未完成 %s: %s（其他项继续）", a.RelPath, res.Error)
		return res
	}
	res.OK = true
	if attempts > 1 {
		res.Detail += fmt.Sprintf("；第 %d 次尝试成功", attempts)
	}
	e.logf("%s %s", strings.ToUpper(phase), a.RelPath)
	return res
}

func (e *Executor) checkLocal(a PlannedAction) error {
	le := LocalEntry{AbsPath: a.LocalPath, RelPath: a.RelPath, Size: a.LocalSize, Mtime: a.LocalMtime, MtimeNano: a.LocalMtimeNano}
	verify := e.LocalVerified
	if verify == nil {
		verify = CheckLocalUnchanged
	}
	if err := verify(le); err != nil {
		return changed("本地文件已变化，跳过此项: %v", err)
	}
	return nil
}

func (e *Executor) renameOne(plan *Plan, res *ApplyResult) error {
	a := res.Action
	if err := checkLocalRoot(plan); err != nil {
		return err
	}
	if err := e.checkLocal(a); err != nil {
		return err
	}
	if a.Hash == nil {
		return changed("缺少本地内容哈希，无法确认重命名版本")
	}
	target, terr := e.Writer.Meta(a.RemotePath)
	source, serr := e.Writer.Meta(a.SourcePath)
	if terr != nil && !IsNotFound(terr) {
		return terr
	}
	if serr != nil && !IsNotFound(serr) {
		return serr
	}
	if terr == nil && matchesHash(target, a.Hash, a.LocalSize) {
		res.verifiedTarget = &target
		if IsNotFound(serr) {
			res.Detail = "目标已是本地版本，来源已移走，确认完成"
			return nil
		}
		if expected, ok := plan.cloudSnapshot[a.SourcePath]; ok && sameRemote(expected, source) && matchesHash(source, a.Hash, a.LocalSize) {
			res.DeferredSource = true
			return changed("目标已是本地版本，来源副本保留；可仅重试该副本的清理")
		}
		return changed("目标已被其他任务更新，来源状态也已变化，保留此项")
	}
	if IsNotFound(serr) && IsNotFound(terr) {
		return asyncPending()
	}
	expected, known := plan.cloudSnapshot[a.SourcePath]
	if serr != nil || !known || !sameRemote(expected, source) || !matchesHash(source, a.Hash, a.LocalSize) {
		return changed("重命名来源已变化或缺失，保留其他版本")
	}
	if terr == nil {
		old, existed := plan.cloudSnapshot[a.RemotePath]
		if !existed || !sameRemote(old, target) {
			return changed("同名目标在预览后已变化，跳过此项")
		}
		if target.IsDir {
			return changed("目标变成目录，拒绝替换")
		}
		if err := e.Writer.Remove(a.RemotePath); err != nil {
			return err
		}
		_, err := e.Writer.Meta(a.RemotePath)
		if err == nil {
			return asyncPending()
		}
		if !IsNotFound(err) {
			return err
		}
		res.TargetRemoved = true
		res.Detail = "同名旧版已移入回收站"
	} else if _, existed := plan.cloudSnapshot[a.RemotePath]; existed {
		res.TargetRemoved = true
	}
	// Recheck immediately before moving: retries can span official-client work.
	if err := e.checkLocal(a); err != nil {
		return err
	}
	source, serr = e.Writer.Meta(a.SourcePath)
	if serr != nil {
		return serr
	}
	if !sameRemote(expected, source) || !matchesHash(source, a.Hash, a.LocalSize) {
		return changed("来源在重命名前发生变化，保留此项")
	}
	if _, err := e.Writer.Meta(a.RemotePath); err == nil {
		return changed("目标在操作期间重新出现，跳过以避免覆盖")
	} else if !IsNotFound(err) {
		return err
	}
	if err := e.Writer.Move(a.SourcePath, a.RemotePath); err != nil {
		return err
	}
	target, terr = e.Writer.Meta(a.RemotePath)
	if terr != nil {
		if IsNotFound(terr) {
			return asyncPending()
		}
		return terr
	}
	if !matchesHash(target, a.Hash, a.LocalSize) {
		return changed("重命名后的目标内容不一致，保留关联历史副本")
	}
	res.verifiedTarget = &target
	_, serr = e.Writer.Meta(a.SourcePath)
	if serr == nil {
		return asyncPending()
	}
	if !IsNotFound(serr) {
		return serr
	}
	res.Detail += "；已重命名并核对目标内容"
	return nil
}

func (e *Executor) removeOne(plan *Plan, res *ApplyResult) error {
	a := res.Action
	if err := checkLocalRoot(plan); err != nil {
		return err
	}
	localPath := filepath.Join(plan.LocalRoot, filepath.FromSlash(a.RelPath))
	if _, err := os.Lstat(localPath); err == nil {
		return changed("本地同路径已出现，保留云端此项")
	} else if !os.IsNotExist(err) {
		return changed("无法确认本地路径是否已删除: %v", err)
	}
	if a.Requires != "" {
		if err := e.checkLocal(a); err != nil {
			return err
		}
		latest, err := e.Writer.Meta(a.Requires)
		if err != nil {
			return err
		}
		if !matchesHash(latest, a.Hash, a.LocalSize) {
			return changed("当前本地版本尚未在云端就位，保留历史副本")
		}
	}
	actual, err := e.Writer.Meta(a.RemotePath)
	if IsNotFound(err) {
		res.Detail = "云端已不存在，确认完成"
		return nil
	}
	if err != nil {
		return err
	}
	expected, known := plan.cloudSnapshot[a.RemotePath]
	if !known || !sameRemote(expected, actual) {
		return changed("云端此项在预览后发生变化，跳过")
	}
	if a.IsDir && !a.WholeDir {
		scan, err := ScanCloud(e.Writer, a.RemotePath, ScanLimits{Workers: 1, Progress: e.Progress})
		if err != nil {
			return err
		}
		if !scan.ScanComplete() {
			return changed("待删除目录未完整读取，跳过该目录")
		}
		want := make(map[string]RemoteEntry)
		for p, en := range plan.cloudSnapshot {
			if strings.HasPrefix(p, a.RemotePath+"/") {
				want[p] = en
			}
		}
		if !sameCloudTree(want, scan.Entries) {
			return changed("待删除目录内有变化，保留整个目录")
		}
	}
	// Scan and API calls may have taken time; do not delete a reappeared local
	// path, even when it was absent at the start of this attempt.
	if _, err := os.Lstat(localPath); err == nil {
		return changed("本地同路径已出现，跳过")
	} else if !os.IsNotExist(err) {
		return changed("本地路径不可确认，跳过")
	}
	if err := checkLocalRoot(plan); err != nil {
		return err
	}
	if err := e.Writer.Remove(a.RemotePath); err != nil {
		return err
	}
	if _, err := e.Writer.Meta(a.RemotePath); err == nil {
		return asyncPending()
	} else if !IsNotFound(err) {
		return err
	}
	res.Detail = "已移入网盘回收站"
	return nil
}

func checkLocalRoot(plan *Plan) error {
	st, err := os.Stat(plan.LocalRoot)
	if err != nil || !st.IsDir() {
		return changed("本地根目录当前不可访问，禁止清理: %s", plan.LocalRoot)
	}
	return nil
}

func (e *Executor) blastRadiusRefusal(plan *Plan, opt ApplyOptions) string {
	if opt.AcknowledgeBlast {
		return ""
	}
	if n := plan.UnscannedRemoveDirs(); n > 0 {
		return fmt.Sprintf("拟整目录回收 %d 项，内部数量与容量未统计，无法判断 10%%/10GB 阈值；请确认包括其全部内容", n)
	}
	if opt.MaxRemovePercent > 0 && plan.CloudFiles > 0 {
		pct := float64(plan.RemoveFiles()) / float64(plan.CloudFiles) * 100
		if pct > opt.MaxRemovePercent {
			return fmt.Sprintf("拟清理 %d 个文件，占云端 %.1f%%，超过阈值 %.1f%%，需要额外确认", plan.RemoveFiles(), pct, opt.MaxRemovePercent)
		}
	}
	if opt.MaxRemoveBytes > 0 && plan.RemoveBytes() > opt.MaxRemoveBytes {
		return fmt.Sprintf("拟清理 %s，超过阈值，需要额外确认", converter.ConvertFileSize(plan.RemoveBytes(), 2))
	}
	return ""
}

// RemainingPlan keeps only unfinished independent actions. Completed actions
// are not replayed, and a rename whose old target was removed remains resumable.
func RemainingPlan(plan *Plan, rep *ApplyReport) *Plan {
	if plan == nil || rep == nil || rep.Aborted {
		return nil
	}
	remaining := *plan
	remaining.Performance = PlanPerformance{}
	remaining.Renames = nil
	remaining.Removes = nil
	remaining.cloudSnapshot = make(map[string]RemoteEntry, len(plan.cloudSnapshot))
	for p, e := range plan.cloudSnapshot {
		remaining.cloudSnapshot[p] = e
	}
	// Keep the retained scan consistent with successful mutations, so a later
	// failed-scope rescan cannot reintroduce already completed operations.
	for _, res := range rep.Removes {
		if res.OK {
			removeRemoteScope(remaining.cloudSnapshot, res.Action.RemotePath)
		}
	}
	for _, res := range rep.Renames {
		if res.verifiedTarget != nil {
			remaining.cloudSnapshot[res.Action.RemotePath] = *res.verifiedTarget
		}
		if res.OK {
			if source, ok := remaining.cloudSnapshot[res.Action.SourcePath]; ok {
				delete(remaining.cloudSnapshot, res.Action.SourcePath)
				if res.verifiedTarget == nil {
					source.Path, source.Name = res.Action.RemotePath, path.Base(res.Action.RemotePath)
					remaining.cloudSnapshot[source.Path] = source
				}
			}
		}
		if res.TargetRemoved && !res.OK && res.verifiedTarget == nil {
			delete(remaining.cloudSnapshot, res.Action.RemotePath)
		}
		if res.DeferredSource {
			a := res.Action
			a.Kind = ActionRemove
			a.Requires = a.RemotePath
			a.RemotePath = a.SourcePath
			a.RelPath = RelPath(plan.RemoteRoot, a.SourcePath)
			a.SourcePath = ""
			a.Reason = "正确版本已就位，清理剩余来源副本"
			remaining.Removes = append(remaining.Removes, a)
		} else if !res.OK {
			remaining.Renames = append(remaining.Renames, res.Action)
		}
	}
	for _, res := range rep.Removes {
		if !res.OK {
			remaining.Removes = append(remaining.Removes, res.Action)
		}
	}
	if rep.RemovalSkipped {
		remaining.Renames = append([]PlannedAction(nil), plan.Renames...)
		remaining.Removes = append([]PlannedAction(nil), plan.Removes...)
	}
	if remaining.Changes() == 0 && len(remaining.ScanIssues) == 0 {
		return nil
	}
	return &remaining
}

func ParentRemote(p string) string { p = NormalizeRemoteRoot(p); return path.Dir(p) }

const ApplyVersion = "0.4.2"
