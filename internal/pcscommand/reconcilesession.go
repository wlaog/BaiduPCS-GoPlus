package pcscommand

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile"
)

// ReconcileSession is the programmatic surface behind the CLI and the local GUI.
//
// Both front-ends drive the same three operations, so the GUI cannot diverge
// from the command line: there is exactly one implementation of probing,
// planning and applying. Nothing here renders or prompts - callers decide how to
// present progress, which is what keeps the GUI decoupled from the safety logic.
type ReconcileSession struct {
	Progress reconcile.ProgressFunc
	// Observer receives progress lines. Nil discards them.
	Observer func(string)
}

// AccountState describes the signed-in account without exposing credentials.
type AccountState struct {
	LoggedIn bool   `json:"loggedIn"`
	Name     string `json:"name,omitempty"`
	UID      uint64 `json:"uid,omitempty"`
	// ConfigDir is where BaiduPCS-Go keeps its state for this session.
	ConfigDir string `json:"configDir"`
}

func (s *ReconcileSession) log(msg string) {
	if s != nil && s.Observer != nil {
		s.Observer(msg)
	}
}

// ensureLogin returns an error when no account is signed in.
//
// GetActiveUser never returns nil (it yields a zero value), so an empty name is
// the real "not logged in" signal.
func ensureLogin() (*pcsconfig.Baidu, error) {
	user := GetActiveUser()
	if user == nil || strings.TrimSpace(user.Name) == "" {
		return nil, errors.New("未登录百度账号。请先在客户端登录，或使用命令行执行: BaiduPCS-Go login")
	}
	return user, nil
}

// Account reports the current sign-in state.
func (s *ReconcileSession) Account() AccountState {
	state := AccountState{ConfigDir: pcsconfig.GetConfigDir()}
	user := GetActiveUser()
	if user != nil && strings.TrimSpace(user.Name) != "" {
		state.LoggedIn = true
		state.Name = user.Name
		state.UID = user.UID
	}
	return state
}

// Probe runs the read-only feasibility reconnaissance.
func (s *ReconcileSession) Probe(opt ReconcileProbeOptions) (*reconcile.ProbeReport, error) {
	if _, err := ensureLogin(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.RemoteRoot) == "" && !opt.DiscoverRoots {
		return nil, errors.New("请指定云端目录，或启用「扫描云端根目录」以便自动发现备份目录")
	}
	if strings.TrimSpace(opt.RemoteRoot) == "" {
		opt.RemoteRoot = "/"
		opt.ShallowScan = true
	}
	if opt.HashSamples <= 0 {
		opt.HashSamples = 10
	}

	s.log(fmt.Sprintf("只读探测: remote=%s local=%s", opt.RemoteRoot, opt.LocalDir))
	fs := reconcile.NewRemoteFS(GetBaiduPCS())
	fs.Log = s.log
	info := &reconcile.UserInfo{}
	if u := GetActiveUser(); u != nil {
		info.Name, info.UID = u.Name, u.UID
	}
	report, err := reconcile.RunProbe(fs, info, reconcile.ProbeOptions{
		RemoteRoot:    opt.RemoteRoot,
		LocalDir:      opt.LocalDir,
		HashSamples:   opt.HashSamples,
		DiscoverRoots: opt.DiscoverRoots,
		Redact:        opt.Redact,
		ShallowScan:   opt.ShallowScan,
		Limits:        reconcile.ScanLimits{Workers: 4, Progress: s.Progress},
	})
	if err != nil {
		return nil, err
	}
	s.log("探测完成")
	return report, nil
}

// Plan builds a difference plan. It performs no mutation.
func (s *ReconcileSession) Plan(opt ReconcilePlanOptions) (*reconcile.PlanReport, *reconcile.Plan, error) {
	if _, err := ensureLogin(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(opt.LocalRoot) == "" {
		return nil, nil, errors.New("请指定本地目录")
	}
	if strings.TrimSpace(opt.RemoteRoot) == "" {
		return nil, nil, errors.New("请指定云端目录")
	}

	s.log(fmt.Sprintf("扫描两端: local=%s remote=%s", opt.LocalRoot, opt.RemoteRoot))
	started := time.Now()
	cachePath := filepath.Join(pcsconfig.GetConfigDir(), "reconcile-hashes.json")
	cache := reconcile.LoadHashCache(cachePath)
	fs := reconcile.NewRemoteFS(GetBaiduPCS())
	fs.Log = s.log
	plan, err := reconcile.BuildPlan(fs, reconcile.PlanOptions{
		Progress: s.Progress, HashCache: cache, UseHashCache: !opt.HashFiles,
		Limits:       reconcile.ScanLimits{Workers: 4},
		LocalRoot:    opt.LocalRoot,
		RemoteRoot:   opt.RemoteRoot,
		HashFiles:    opt.HashFiles,
		Conservative: opt.Conservative,
		SkipSuffixes: splitList(opt.SkipSuffixes),
		SkipNames:    splitList(opt.SkipNames),
	})
	if err != nil {
		return nil, nil, err
	}
	if err := cache.Save(cachePath); err != nil {
		s.log("哈希缓存保存失败，下次将重新计算")
	}
	s.log(fmt.Sprintf("扫描完成, 用时 %s", time.Since(started).Round(time.Second)))
	s.log(fmt.Sprintf("本次内容读取 %d 字节，复用 %d 个哈希缓存，云端目录请求 %d 次；待重命名 %d，待清理 %d，等待官方备份 %d", plan.Performance.HashBytes, plan.Performance.CacheHits, plan.Performance.CloudListings, len(plan.Renames), len(plan.Removes), len(plan.Pending)))
	return reconcile.BuildPlanReport(plan, "plan (只读预览)"), plan, nil
}

// ApplyOptions drives an execution from a front-end.
type ApplyOptions struct {
	// Plan is the plan to execute; it is re-verified before any write.
	Plan *reconcile.Plan
	// RemovalMode is quarantine (default) or recycle.
	RemovalMode string
	// QuarantineRoot overrides the default quarantine directory.
	QuarantineRoot string
	// ConfirmRemoval authorises the removal phase.
	ConfirmRemoval bool
	// AcknowledgeRecycle is additionally required for the recycle mode.
	AcknowledgeRecycle bool
	// AcknowledgeBlast authorises a removal beyond the thresholds.
	AcknowledgeBlast bool
	// MaxRemovePercent / MaxRemoveBytes are the blast-radius thresholds.
	MaxRemovePercent float64
	MaxRemoveBytes   int64
}

// Apply executes a plan.
func (s *ReconcileSession) Apply(opt ApplyOptions) (*reconcile.ApplyReport, error) {
	if _, err := ensureLogin(); err != nil {
		return nil, err
	}
	if opt.Plan == nil {
		return nil, errors.New("缺少差异计划: 请先执行「预览差异」")
	}
	if !opt.Plan.ScanComplete || !opt.Plan.LocalScanComplete {
		return nil, errors.New("扫描不完整, 已拒绝执行 (不会做任何改动)。请重新预览差异后再试")
	}

	mode := reconcile.RemovalMode(strings.ToLower(strings.TrimSpace(opt.RemovalMode)))
	switch mode {
	case "":
		mode = reconcile.RemovalRecycle
	case reconcile.RemovalRecycle:
	default:
		return nil, fmt.Errorf("未知的清理方式 %q", opt.RemovalMode)
	}
	if mode == reconcile.RemovalRecycle && opt.ConfirmRemoval && !opt.AcknowledgeRecycle {
		return nil, errors.New("回收站模式需要额外确认")
	}

	s.log(fmt.Sprintf("开始执行 (清理方式: %s)", mode))
	writer := reconcile.NewRemoteWriter(GetBaiduPCS())
	writer.Log = s.log
	executor := &reconcile.Executor{Writer: writer, Log: s.log, Progress: s.Progress}

	return executor.Apply(opt.Plan, reconcile.ApplyOptions{
		PlanOptions: reconcile.PlanOptions{
			LocalRoot:  opt.Plan.LocalRoot,
			RemoteRoot: opt.Plan.RemoteRoot,
			HashFiles:  true,
		},
		RemovalMode:        mode,
		QuarantineRoot:     opt.QuarantineRoot,
		ConfirmRemoval:     opt.ConfirmRemoval,
		AcknowledgeRecycle: opt.AcknowledgeRecycle,
		MaxRemovePercent:   opt.MaxRemovePercent,
		MaxRemoveBytes:     opt.MaxRemoveBytes,
		AcknowledgeBlast:   opt.AcknowledgeBlast,
	})
}

// SaveReconcileReport persists a JSON payload under the report directory and
// returns the file path. Front-ends use it so every GUI action also leaves the
// same on-disk record the CLI produces.
func SaveReconcileReport(name string, payload interface{}) (string, error) {
	dir := reconcileReportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return writeJSONReport(dir, name, payload)
}

// SaveProbeReport persists a probe report.
func SaveProbeReport(r *reconcile.ProbeReport) (string, error) {
	return reconcile.WriteProbeReport(reconcileReportDir(), r)
}

// SavePlanReport persists a plan report.
func SavePlanReport(r *reconcile.PlanReport) (string, error) {
	return reconcile.WritePlanReport(reconcileReportDir(), r)
}

// SaveApplyReport persists an apply report.
func SaveApplyReport(r *reconcile.ApplyReport) (string, error) {
	return reconcile.WriteApplyReport(reconcileReportDir(), r)
}
