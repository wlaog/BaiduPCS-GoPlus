package pcscommand

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsverbose"
)

var reconcileVerbose = pcsverbose.New("RECONCILE")

// ReconcileProbeOptions mirrors the CLI flags for the read-only probe. It is
// kept separate from reconcile.ProbeOptions so flag plumbing stays in the
// command layer and the reconcile package stays free of CLI concerns.
type ReconcileProbeOptions struct {
	RemoteRoot    string
	LocalDir      string
	HashSamples   int
	DiscoverRoots bool
	Redact        bool
	JSONOut       string
	// ShallowScan limits the scan to the direct children of RemoteRoot. It is
	// what --discover-roots uses by default: enumerating the entire account just
	// to list the top-level backup folders is slow and can trip Baidu's rate
	// limiting.
	ShallowScan bool
}

// reconcileReportDir returns the directory where reports are written. It lives
// next to the program's config so reports do not leak into the user's tree.
func reconcileReportDir() string {
	return filepath.Join(pcsconfig.GetConfigDir(), "reconcile-reports")
}

// ReconcilePlanOptions mirrors the CLI flags for the read-only difference
// preview. Planning performs no mutation: it reads both trees and decides what
// would change, so it is safe to run against a live backup directory.
type ReconcilePlanOptions struct {
	LocalRoot string
	RemoteRoot string
	// HashFiles computes content hashes to upgrade "same size" comparisons into
	// a proof of equality or difference.
	HashFiles bool
	// Conservative reports "same size, untrusted hash" pairs as CONFLICT
	// instead of planning a re-upload.
	Conservative bool
	// SkipSuffixes / SkipNames are comma-separated exclusion lists. Empty means
	// the defaults.
	SkipSuffixes string
	SkipNames    string
	// JSONOut is the directory for the saved report.
	JSONOut string
	// ShowAll prints every item rather than truncating each section.
	ShowAll bool
}

// RunReconcilePlan builds and prints the difference plan without changing
// anything.
func RunReconcilePlan(opt *ReconcilePlanOptions) error {
	if opt == nil {
		return fmt.Errorf("缺少参数")
	}
	if strings.TrimSpace(opt.LocalRoot) == "" {
		return fmt.Errorf("必须指定 --local <本地目录>")
	}
	if strings.TrimSpace(opt.RemoteRoot) == "" {
		return fmt.Errorf("必须指定 --remote <云端目录>")
	}

	user := GetActiveUser()
	if user == nil || user.Name == "" {
		return errors.New("未登录百度账号, 请先执行: BaiduPCS-Go login")
	}

	fs := reconcile.NewRemoteFS(GetBaiduPCS())
	started := time.Now()

	plan, err := reconcile.BuildPlan(fs, reconcile.PlanOptions{
		LocalRoot:    opt.LocalRoot,
		RemoteRoot:   opt.RemoteRoot,
		HashFiles:    opt.HashFiles,
		Conservative: opt.Conservative,
		SkipSuffixes: splitList(opt.SkipSuffixes),
		SkipNames:    splitList(opt.SkipNames),
	})
	if err != nil {
		return err
	}

	report := reconcile.BuildPlanReport(plan, "plan (只读预览)")
	reconcile.RenderPlanHuman(os.Stdout, report)

	outDir := opt.JSONOut
	if outDir == "" {
		outDir = reconcileReportDir()
	}
	path, writeErr := reconcile.WritePlanReport(outDir, report)
	if writeErr != nil {
		fmt.Printf("\n警告: 未能写入 JSON 报告: %v\n", writeErr)
	} else {
		fmt.Printf("\nJSON 报告已保存: %s\n", path)
	}
	fmt.Printf("耗时: %s\n", time.Since(started).Round(time.Millisecond))
	if report.Summary.ZeroDifference && report.Counts.Conflict == 0 {
		fmt.Printf("结果: 零差异 — 云端已与本地一致。\n")
	} else {
		fmt.Printf("结果: 待处理 %d 项变更, %d 项冲突。\n",
			report.Summary.Changes, report.Counts.Conflict)
	}
	return nil
}

// splitList turns a comma-separated flag value into a slice, returning nil for
// an empty string so the callee applies its defaults.
func splitList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ReconcileApplyOptions mirrors the CLI flags for execution.
type ReconcileApplyOptions struct {
	ReconcilePlanOptions
	// RemovalMode is "quarantine" (default) or "recycle".
	RemovalMode string
	// QuarantineRoot overrides the default quarantine directory.
	QuarantineRoot string
	// ConfirmRemoval authorises the removal phase.
	ConfirmRemoval bool
	// AcknowledgeRecycle is additionally required for the recycle mode.
	AcknowledgeRecycle bool
	// AcknowledgeBlast authorises a removal beyond the blast-radius thresholds.
	AcknowledgeBlast bool
	// MaxRemovePercent / MaxRemoveBytes are the thresholds.
	MaxRemovePercent float64
	MaxRemoveBytes   int64
	// MaxDepth bounds cloud enumeration.
	MaxDepth int
}

// RunReconcileApply builds a plan and executes it.
//
// It re-plans immediately before acting so the decisions are made against the
// current cloud state rather than a possibly stale file, and it refuses to run
// at all when the plan rests on an incomplete scan.
func RunReconcileApply(opt *ReconcileApplyOptions) error {
	if opt == nil {
		return fmt.Errorf("缺少参数")
	}
	if strings.TrimSpace(opt.LocalRoot) == "" {
		return fmt.Errorf("必须指定 --local <本地目录>")
	}
	if strings.TrimSpace(opt.RemoteRoot) == "" {
		return fmt.Errorf("必须指定 --remote <云端目录>")
	}

	user := GetActiveUser()
	if user == nil || user.Name == "" {
		return errors.New("未登录百度账号, 请先执行: BaiduPCS-Go login")
	}

	mode := reconcile.RemovalMode(strings.ToLower(strings.TrimSpace(opt.RemovalMode)))
	switch mode {
	case "":
		mode = reconcile.RemovalQuarantine
	case reconcile.RemovalQuarantine, reconcile.RemovalRecycle:
	default:
		return fmt.Errorf("未知的清理方式 %q: 可选 %s 或 %s",
			opt.RemovalMode, reconcile.RemovalQuarantine, reconcile.RemovalRecycle)
	}

	pcs := GetBaiduPCS()
	started := time.Now()

	fmt.Printf("正在扫描两端并生成计划 (执行前重新校验)...\n")
	plan, err := reconcile.BuildPlan(reconcile.NewRemoteFS(pcs), reconcile.PlanOptions{
		LocalRoot:    opt.LocalRoot,
		RemoteRoot:   opt.RemoteRoot,
		HashFiles:    opt.HashFiles,
		Conservative: opt.Conservative,
		SkipSuffixes: splitList(opt.SkipSuffixes),
		SkipNames:    splitList(opt.SkipNames),
	})
	if err != nil {
		return err
	}

	// Show the preview before acting; the operator agreed to a shape of change
	// and should see the one actually being applied.
	preview := reconcile.BuildPlanReport(plan, "apply (执行前预览)")
	reconcile.RenderPlanHuman(os.Stdout, preview)

	if !plan.ScanComplete || !plan.LocalScanComplete {
		return errors.New("扫描不完整, 已拒绝执行 (不会做任何改动)")
	}
	if plan.Changes() == 0 {
		fmt.Printf("\n零差异: 云端已与本地一致, 无需执行。\n")
		return nil
	}

	// Interactive confirmation, showing what is at stake.
	if !opt.ConfirmRemoval && len(plan.Removes) > 0 {
		fmt.Printf("\n本次将清理 %d 项 (%d 文件 / %d 目录), 合计 %s。\n",
			len(plan.Removes), plan.RemoveFiles(), len(plan.Removes)-plan.RemoveFiles(),
			converter.ConvertFileSize(plan.RemoveBytes(), 2))
		if mode == reconcile.RemovalQuarantine {
			fmt.Printf("它们会被移动到隔离区 (不会永久删除, 可随时移回)。\n")
		} else {
			fmt.Printf("它们会被移入网盘回收站 (保留约 35 天)。\n")
		}
		if !promptYes("确认继续? 输入 yes 继续, 其他任意输入取消 > ") {
			fmt.Printf("已取消, 未做任何改动。\n")
			return nil
		}
		opt.ConfirmRemoval = true
		if mode == reconcile.RemovalRecycle {
			opt.AcknowledgeRecycle = true
		}
	}

	writer := reconcile.NewRemoteWriter(pcs)
	executor := &reconcile.Executor{
		Writer: writer,
		Log:    func(s string) { fmt.Printf("  %s\n", s) },
	}

	fmt.Printf("\n开始执行...\n")
	report, err := executor.Apply(plan, reconcile.ApplyOptions{
		PlanOptions: reconcile.PlanOptions{
			LocalRoot:    opt.LocalRoot,
			RemoteRoot:   opt.RemoteRoot,
			HashFiles:    opt.HashFiles,
			Conservative: opt.Conservative,
			SkipSuffixes: splitList(opt.SkipSuffixes),
			SkipNames:    splitList(opt.SkipNames),
		},
		RemovalMode:      mode,
		QuarantineRoot:   opt.QuarantineRoot,
		ConfirmRemoval:   opt.ConfirmRemoval,
		AcknowledgeRecycle: opt.AcknowledgeRecycle,
		MaxRemovePercent: opt.MaxRemovePercent,
		MaxRemoveBytes:   opt.MaxRemoveBytes,
		AcknowledgeBlast: opt.AcknowledgeBlast,
	})
	if err != nil {
		return err
	}

	fmt.Printf("\n")
	reconcile.RenderApplyHuman(os.Stdout, report)

	outDir := opt.JSONOut
	if outDir == "" {
		outDir = reconcileReportDir()
	}
	if path, writeErr := reconcile.WriteApplyReport(outDir, report); writeErr != nil {
		fmt.Printf("\n警告: 未能写入执行记录: %v\n", writeErr)
	} else {
		fmt.Printf("\n执行记录已保存: %s\n", path)
	}
	fmt.Printf("耗时: %s\n", time.Since(started).Round(time.Millisecond))
	return nil
}

// promptYes reads one line from stdin and reports whether it is an explicit
// affirmative. Anything else, including EOF, means no.
func promptYes(label string) bool {
	fmt.Print(label)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}

// RunReconcileProbe executes the read-only feasibility probe.
//
// This command performs no cloud mutation of any kind. It is safe to run against
// a real backup directory, because the reconcile package's read path is built
// against an interface that exposes only listing and metadata.
func RunReconcileProbe(opt *ReconcileProbeOptions) error {
	if opt == nil {
		return fmt.Errorf("缺少参数")
	}
	if opt.RemoteRoot == "" {
		return fmt.Errorf("必须指定 --remote <云端目录>")
	}

	user := GetActiveUser()
	if user == nil || user.Name == "" {
		return errors.New("未登录百度账号, 请先执行: BaiduPCS-Go login")
	}

	fs := reconcile.NewRemoteFS(GetBaiduPCS())

	info := &reconcile.UserInfo{Name: user.Name, UID: user.UID}

	started := time.Now()
	reconcileVerbose.Infof("开始只读探测: remote=%s local=%s\n", opt.RemoteRoot, opt.LocalDir)

	report, err := reconcile.RunProbe(fs, info, reconcile.ProbeOptions{
		RemoteRoot:    opt.RemoteRoot,
		LocalDir:      opt.LocalDir,
		HashSamples:   opt.HashSamples,
		DiscoverRoots: opt.DiscoverRoots,
		Redact:        opt.Redact,
		ShallowScan:   opt.ShallowScan,
	})
	if err != nil {
		return err
	}

	reconcile.RenderProbeHuman(os.Stdout, report)

	// Always persist the JSON report: the acceptance criteria ask for a saved,
	// reviewable record of every run.
	outDir := opt.JSONOut
	if outDir == "" {
		outDir = reconcileReportDir()
	}
	path, writeErr := reconcile.WriteProbeReport(outDir, report)
	if writeErr != nil {
		fmt.Printf("\n警告: 未能写入 JSON 报告: %v\n", writeErr)
	} else {
		fmt.Printf("\nJSON 报告已保存: %s\n", path)
	}
	fmt.Printf("耗时: %s\n", time.Since(started).Round(time.Millisecond))
	// Scan is nil when the run aborted before enumerating anything (for example
	// an unreachable or misspelled remote root). Report that plainly instead of
	// dereferencing it.
	if report.Scan == nil {
		fmt.Printf("云端统计: 未完成枚举 — 见上方「错误」与「判定」\n")
		return nil
	}
	fmt.Printf("云端统计: 目录 %d / 文件 %d / %s\n",
		report.Scan.Stats.DirCount, report.Scan.Stats.FileCount,
		converter.ConvertFileSize(report.Scan.Stats.TotalBytes, 2))

	return nil
}
