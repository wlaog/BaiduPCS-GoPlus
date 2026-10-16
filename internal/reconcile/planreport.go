package reconcile

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
)

// PlanReport is the serialisable preview of a plan.
type PlanReport struct {
	PlanVersion  string `json:"planVersion"`
	GeneratedAt  string `json:"generatedAt"`
	Mode         string `json:"mode"`
	LocalRoot    string `json:"localRoot"`
	RemoteRoot   string `json:"remoteRoot"`
	LocalExists  bool   `json:"localRootExists"`

	StructuralDigest string `json:"cloudStructuralDigest"`
	ScanComplete     bool   `json:"cloudScanComplete"`
	LocalScanComplete bool  `json:"localScanComplete"`

	Summary PlanSummary `json:"summary"`
	Counts  PlanCounts  `json:"counts"`

	Uploads   []ReportAction `json:"uploads"`
	Replaces  []ReportAction `json:"replaces"`
	Mkdirs    []ReportAction `json:"mkdirs"`
	Removes   []ReportAction `json:"removes"`
	Keeps     []ReportAction `json:"keeps"`
	Conflicts []ReportAction `json:"conflicts"`

	ScanErrors  []string `json:"cloudScanErrors,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	SkipNotices []string `json:"skipNotices,omitempty"`
	// Blockers list conditions that forbid turning this plan into changes.
	Blockers []string `json:"blockers,omitempty"`
	// KeptOmitted counts KEEP entries dropped from the report to keep it small.
	KeptOmitted int `json:"keepsOmitted"`
}

// PlanCounts is the machine-readable tally.
type PlanCounts struct {
	Upload  int `json:"upload"`
	Replace int `json:"replace"`
	Mkdir   int `json:"mkdir"`
	Remove  int `json:"remove"`
	Keep    int `json:"keep"`
	Conflict int `json:"conflict"`
}

// PlanSummary carries the headline numbers an operator needs before deciding.
type PlanSummary struct {
	Changes            int   `json:"changes"`
	UploadBytes        int64 `json:"uploadBytes"`
	RemoveFiles        int   `json:"removeFiles"`
	RemoveDirs         int   `json:"removeDirs"`
	RemoveBytes        int64 `json:"removeBytes"`
	LocalFiles         int   `json:"localFiles"`
	LocalDirs          int   `json:"localDirs"`
	LocalBytes         int64 `json:"localBytes"`
	CloudFiles         int   `json:"cloudFiles"`
	CloudDirs          int   `json:"cloudDirs"`
	CloudBytes         int64 `json:"cloudBytes"`
	ZeroDifference     bool  `json:"zeroDifference"`
}

// ReportAction is one action as serialised into the report.
type ReportAction struct {
	Kind       string `json:"kind"`
	RelPath    string `json:"relPath"`
	RemotePath string `json:"remotePath"`
	LocalPath  string `json:"localPath,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SizeText   string `json:"sizeText,omitempty"`
	IsDir      bool   `json:"isDir,omitempty"`
	Rule       string `json:"rule,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// PlanVersion identifies the plan implementation in saved reports.
const PlanVersion = "0.1.0"

// KeepsShownInReport bounds how many KEEP rows are serialised, since a large
// mirror can have hundreds of thousands of them and the operator only needs the
// counts plus the ability to spot-check.
const KeepsShownInReport = 200

// BuildPlanReport turns a plan into its report form. Mode is the user-visible
// label, e.g. "plan (dry-run)".
func BuildPlanReport(p *Plan, mode string) *PlanReport {
	rep := &PlanReport{
		PlanVersion:      PlanVersion,
		GeneratedAt:      p.GeneratedAt,
		Mode:             mode,
		LocalRoot:        p.LocalRoot,
		RemoteRoot:       p.RemoteRoot,
		StructuralDigest: p.StructuralDigest,
		ScanComplete:     p.ScanComplete,
		LocalScanComplete: p.LocalScanComplete,
		ScanErrors:       p.ScanErrors,
		Warnings:         p.Warnings,
		SkipNotices:      p.SkipNotices,
	}
	if _, err := os.Stat(p.LocalRoot); err == nil {
		rep.LocalExists = true
	}

	rep.Uploads = toReportActions(p.Uploads)
	rep.Replaces = toReportActions(p.Replaces)
	rep.Mkdirs = toReportActions(p.Mkdirs)
	rep.Removes = toReportActions(p.Removes)
	rep.Conflicts = toReportActions(p.Conflicts)

	keeps := p.Keeps
	if len(keeps) > KeepsShownInReport {
		rep.KeptOmitted = len(keeps) - KeepsShownInReport
		keeps = keeps[:KeepsShownInReport]
	}
	rep.Keeps = toReportActions(keeps)

	rep.Counts = PlanCounts{
		Upload:   len(p.Uploads),
		Replace:  len(p.Replaces),
		Mkdir:    len(p.Mkdirs),
		Remove:   len(p.Removes),
		Keep:     len(p.Keeps),
		Conflict: len(p.Conflicts),
	}
	removeDirs := 0
	for _, a := range p.Removes {
		if a.IsDir {
			removeDirs++
		}
	}
	rep.Summary = PlanSummary{
		Changes:        p.Changes(),
		UploadBytes:    p.UploadBytes(),
		RemoveFiles:    p.RemoveFiles(),
		RemoveDirs:     removeDirs,
		RemoveBytes:    p.RemoveBytes(),
		LocalFiles:     p.LocalFiles,
		LocalDirs:      p.LocalDirs,
		LocalBytes:     p.LocalBytes,
		CloudFiles:     p.CloudFiles,
		CloudDirs:      p.CloudDirs,
		CloudBytes:     p.CloudBytes,
		ZeroDifference: p.ZeroDifference(),
	}
	rep.Blockers = collectBlockers(p)
	return rep
}

// collectBlockers lists the conditions that must stop this plan from being
// applied as-is. They are reported for every plan, including dry-runs, so the
// operator learns about them before ever running an apply.
func collectBlockers(p *Plan) []string {
	var out []string
	if !p.ScanComplete {
		out = append(out, fmt.Sprintf(
			"云端目录枚举不完整 (%d 处), 无法确认云端真实内容; 禁止在此结果上执行任何清理", len(p.ScanErrors)))
	}
	if !p.LocalScanComplete {
		out = append(out, "本地目录未完整遍历 (条目数达到上限), 禁止执行清理")
	}
	if p.LocalFiles == 0 && p.LocalDirs == 0 {
		out = append(out, "本地目录为空: 若确认本地未被误清空才可继续, 否则禁止执行清理")
	}
	if len(p.Conflicts) > 0 {
		out = append(out, fmt.Sprintf("存在 %d 个需要人工判断的冲突项, 默认不会自动处理", len(p.Conflicts)))
	}
	for _, w := range p.Warnings {
		_ = w
	}
	return out
}

func toReportActions(actions []PlannedAction) []ReportAction {
	out := make([]ReportAction, 0, len(actions))
	for _, a := range actions {
		ra := ReportAction{
			Kind:       string(a.Kind),
			RelPath:    a.RelPath,
			RemotePath: a.RemotePath,
			LocalPath:  a.LocalPath,
			Size:       a.Size,
			IsDir:      a.IsDir,
			Rule:       string(a.Rule),
			Reason:     a.Reason,
		}
		if a.Size > 0 {
			ra.SizeText = converter.ConvertFileSize(a.Size, 2)
		}
		out = append(out, ra)
	}
	return out
}

// PlanJSON serialises the report.
func (r *PlanReport) PlanJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// WritePlanReport writes the JSON report and returns its path.
func WritePlanReport(dir string, r *PlanReport) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("plan-%s.json", time.Now().Format("20060102-150405"))
	full := filepath.Join(dir, name)
	data, err := r.PlanJSON()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return full, nil
}

// RenderPlanHuman writes the operator-facing preview.
func RenderPlanHuman(w io.Writer, r *PlanReport) {
	p := func(format string, a ...interface{}) { fmt.Fprintf(w, format+"\n", a...) }

	p("=== 对齐本地 · 差异预览 (未改变任何内容) ===")
	p("生成时间: %s   模式: %s", r.GeneratedAt, r.Mode)
	p("本地(权威源): %s", r.LocalRoot)
	p("云端(将被对齐): %s", r.RemoteRoot)
	p("云端结构指纹: %s", r.StructuralDigest)
	p("")

	p("--- 概况 ---")
	p("  本地: 目录 %d / 文件 %d / %s", r.Summary.LocalDirs, r.Summary.LocalFiles,
		converter.ConvertFileSize(r.Summary.LocalBytes, 2))
	p("  云端: 目录 %d / 文件 %d / %s", r.Summary.CloudDirs, r.Summary.CloudFiles,
		converter.ConvertFileSize(r.Summary.CloudBytes, 2))
	p("")

	p("--- 差异汇总 ---")
	p("  UPLOAD  (云端缺失, 需上传):       %d 项, %s", r.Counts.Upload,
		converter.ConvertFileSize(sumSizes(r.Uploads), 2))
	p("  REPLACE (同路径内容不同, 需覆盖): %d 项, %s", r.Counts.Replace,
		converter.ConvertFileSize(sumSizes(r.Replaces), 2))
	p("  MKDIR   (需先创建的云端目录):     %d 项", r.Counts.Mkdir)
	p("  REMOVE  (云端多余, 待清理):       %d 项 (%d 文件 / %d 目录), %s",
		r.Counts.Remove, r.Summary.RemoveFiles, r.Summary.RemoveDirs,
		converter.ConvertFileSize(r.Summary.RemoveBytes, 2))
	p("  KEEP    (已一致):                 %d 项", r.Counts.Keep)
	p("  CONFLICT(需人工判断, 不会自动处理): %d 项", r.Counts.Conflict)
	p("")

	if r.Summary.ZeroDifference && r.Counts.Conflict == 0 {
		p("  ✅ 云端已与本地一致, 无需任何改动。")
		p("")
	}

	section := func(title, note string, items []ReportAction, limit int) {
		if len(items) == 0 {
			return
		}
		p("--- %s (%d) ---", title, len(items))
		if note != "" {
			p("  %s", note)
		}
		shown := items
		if limit > 0 && len(shown) > limit {
			shown = shown[:limit]
		}
		for _, a := range shown {
			suffix := ""
			if a.SizeText != "" {
				suffix = "  " + a.SizeText
			}
			mark := ""
			if a.IsDir {
				mark = "/"
			}
			p("  %-46s%s %s", truncate(a.RelPath+mark, 46), suffix, a.Reason)
		}
		if len(shown) < len(items) {
			p("  ... 其余 %d 项省略 (完整清单见 JSON 报告)", len(items)-len(shown))
		}
		p("")
	}

	section("UPLOAD 将要上传", "", r.Uploads, 30)
	section("REPLACE 将要覆盖", "覆盖会替换云端现有版本; apply 时会把旧版先移入隔离区", r.Replaces, 30)
	section("MKDIR 将要创建的云端目录", "", r.Mkdirs, 20)
	section("REMOVE 云端多余项", "apply 时默认移入隔离区 (不永久删除), 可从隔离区恢复", r.Removes, 40)
	section("CONFLICT 需要你人工判断", "这些项不会被自动处理", r.Conflicts, 30)
	section("KEEP 已一致", "", r.Keeps, 10)

	if len(r.SkipNotices) > 0 {
		p("--- 排除规则说明 ---")
		for _, s := range r.SkipNotices {
			p("  %s", s)
		}
		p("")
	}
	if len(r.Warnings) > 0 {
		p("--- 警告 ---")
		for _, s := range r.Warnings {
			p("  %s", s)
		}
		p("")
	}
	if len(r.ScanErrors) > 0 {
		p("--- 云端枚举错误 ---")
		for _, s := range r.ScanErrors {
			p("  %s", s)
		}
		p("")
	}
	if len(r.Blockers) > 0 {
		p("--- 阻止项 (执行前必须解决) ---")
		for _, s := range r.Blockers {
			p("  ⛔ %s", s)
		}
		p("")
	}

	p("本次为只读预览: 未改动云端与本地任何内容。")
}

func sumSizes(items []ReportAction) int64 {
	var n int64
	for _, a := range items {
		n += a.Size
	}
	return n
}
