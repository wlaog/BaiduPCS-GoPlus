package reconcile

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type PlanReport struct {
	PlanVersion       string          `json:"planVersion"`
	GeneratedAt       string          `json:"generatedAt"`
	Mode              string          `json:"mode"`
	LocalRoot         string          `json:"localRoot"`
	RemoteRoot        string          `json:"remoteRoot"`
	Performance       PlanPerformance `json:"performance"`
	Summary           PlanSummary     `json:"summary"`
	Counts            PlanCounts      `json:"counts"`
	Renames           []ReportAction  `json:"renames"`
	Removes           []ReportAction  `json:"removes"`
	Pending           []ReportAction  `json:"pending"`
	Unverified        []ReportAction  `json:"unverified"`
	ScanIssues        []ScanIssue     `json:"scanIssues,omitempty"`
	PartialScanSafe   bool            `json:"partialScanSafe"`
	CanRetryScan      bool            `json:"canRetryScan"`
	Keeps             []ReportAction  `json:"keeps"`
	Conflicts         []ReportAction  `json:"conflicts"`
	ScanComplete      bool            `json:"cloudScanComplete"`
	LocalScanComplete bool            `json:"localScanComplete"`
	StructuralDigest  string          `json:"cloudStructuralDigest"`
	ScanErrors        []string        `json:"cloudScanErrors,omitempty"`
	Warnings          []string        `json:"warnings,omitempty"`
	SkipNotices       []string        `json:"skipNotices,omitempty"`
	Blockers          []string        `json:"blockers,omitempty"`
	KeptOmitted       int             `json:"keepsOmitted"`
}
type PlanCounts struct {
	Rename     int `json:"rename"`
	Remove     int `json:"remove"`
	Pending    int `json:"pending"`
	Keep       int `json:"keep"`
	Conflict   int `json:"conflict"`
	Unverified int `json:"unverified"`
}
type PlanSummary struct {
	Changes             int   `json:"changes"`
	RemoveFiles         int   `json:"removeFiles"`
	RemoveDirs          int   `json:"removeDirs"`
	UnscannedRemoveDirs int   `json:"unscannedRemoveDirs"`
	RemoveBytes         int64 `json:"removeBytes"`
	LocalFiles          int   `json:"localFiles"`
	LocalDirs           int   `json:"localDirs"`
	LocalBytes          int64 `json:"localBytes"`
	CloudFiles          int   `json:"cloudFiles"`
	CloudDirs           int   `json:"cloudDirs"`
	CloudBytes          int64 `json:"cloudBytes"`
	ZeroDifference      bool  `json:"zeroDifference"`
}
type ReportAction struct {
	Kind       string `json:"kind"`
	RelPath    string `json:"relPath"`
	RemotePath string `json:"remotePath"`
	SourcePath string `json:"sourcePath,omitempty"`
	LocalPath  string `json:"localPath,omitempty"`
	Size       int64  `json:"size,omitempty"`
	IsDir      bool   `json:"isDir,omitempty"`
	WholeDir   bool   `json:"wholeDir,omitempty"`
	Reason     string `json:"reason"`
}

const PlanVersion = "0.4.2"
const KeepsShownInReport = 200

func BuildPlanReport(p *Plan, mode string) *PlanReport {
	keeps := p.Keeps
	if len(keeps) > KeepsShownInReport {
		keeps = keeps[:KeepsShownInReport]
	}
	r := &PlanReport{PlanVersion: PlanVersion, GeneratedAt: p.GeneratedAt, Mode: mode, LocalRoot: p.LocalRoot, RemoteRoot: p.RemoteRoot, Performance: p.Performance,
		Counts:  PlanCounts{Rename: len(p.Renames), Remove: len(p.Removes), Pending: len(p.Pending), Keep: len(p.Keeps), Conflict: len(p.Conflicts)},
		Renames: toReportActions(p.Renames), Removes: toReportActions(p.Removes), Pending: toReportActions(p.Pending), Keeps: toReportActions(keeps), Conflicts: toReportActions(p.Conflicts),
		ScanComplete: p.ScanComplete, LocalScanComplete: p.LocalScanComplete, StructuralDigest: p.StructuralDigest, ScanErrors: p.ScanErrors, Warnings: p.Warnings, SkipNotices: p.SkipNotices, KeptOmitted: len(p.Keeps) - len(keeps)}
	dirs := 0
	for _, a := range p.Removes {
		if a.IsDir {
			dirs++
		}
	}
	r.Summary = PlanSummary{Changes: p.Changes(), RemoveFiles: p.RemoveFiles(), RemoveDirs: dirs, RemoveBytes: p.RemoveBytes(), LocalFiles: p.LocalFiles, LocalDirs: p.LocalDirs, LocalBytes: p.LocalBytes, CloudFiles: p.CloudFiles, CloudDirs: p.CloudDirs, CloudBytes: p.CloudBytes, ZeroDifference: p.ZeroDifference()}
	r.Summary.UnscannedRemoveDirs = p.UnscannedRemoveDirs()
	r.Counts.Unverified = len(p.ScanIssues)
	r.Unverified, r.ScanIssues, r.PartialScanSafe = toReportActions(p.Unverified), p.ScanIssues, p.PartialScanSafe
	r.CanRetryScan = p.localSnapshot != nil && p.cloudScan != nil && len(p.ScanIssues) > 0 && !p.localSnapshot.Stats.Truncated && !p.cloudScan.Stats.Truncated
	r.Blockers = collectBlockers(p)
	return r
}
func collectBlockers(p *Plan) []string {
	var out []string
	if !p.ScanComplete && !p.PartialScanSafe {
		out = append(out, "云端枚举不完整，禁止执行")
	}
	if !p.LocalScanComplete && !p.PartialScanSafe {
		out = append(out, "本地扫描不完整，禁止执行")
	}
	if p.LocalFiles == 0 && p.LocalDirs == 0 {
		out = append(out, "本地目录为空，禁止批量清理")
	}
	if !p.CanApply() && p.ScanComplete && p.LocalScanComplete {
		out = append(out, "扫描校验失败，禁止执行")
	}
	return out
}
func toReportActions(a []PlannedAction) []ReportAction {
	out := make([]ReportAction, 0, len(a))
	for _, v := range a {
		out = append(out, ReportAction{Kind: string(v.Kind), RelPath: v.RelPath, RemotePath: v.RemotePath, SourcePath: v.SourcePath, LocalPath: v.LocalPath, Size: v.Size, IsDir: v.IsDir, WholeDir: v.WholeDir, Reason: v.Reason})
	}
	return out
}
func (r *PlanReport) PlanJSON() ([]byte, error)                 { return json.MarshalIndent(r, "", "  ") }
func WritePlanReport(dir string, r *PlanReport) (string, error) { return saveReport(dir, "plan", r) }
func saveReport(dir, prefix string, payload interface{}) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := filepath.Join(dir, prefix+"-"+time.Now().Format("20060102-150405.000000000")+".json")
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return name, os.WriteFile(name, b, 0600)
}
func RenderPlanHuman(w io.Writer, r *PlanReport) {
	fmt.Fprintf(w, "=== 云端整理 · 只删除和重命名 ===\n本地: %s\n云端: %s\nRENAME %d / REMOVE %d / KEEP %d / WAIT %d / CONFLICT %d\n拟移入回收站 %d 个文件（含重命名前旧版）\n", r.LocalRoot, r.RemoteRoot, r.Counts.Rename, r.Counts.Remove, r.Counts.Keep, r.Counts.Pending, r.Counts.Conflict, r.Summary.RemoveFiles)
	if r.Summary.UnscannedRemoveDirs > 0 {
		fmt.Fprintf(w, "另整目录回收 %d 项及其全部内容（含临时文件）；内部数量与容量未统计，以上文件数仅含已扫描部分\n", r.Summary.UnscannedRemoveDirs)
	}
	for _, items := range [][]ReportAction{r.Renames, r.Removes, r.Pending, r.Unverified, r.Conflicts} {
		for i, a := range items {
			if i >= 40 {
				fmt.Fprintln(w, "其余项见 JSON 报告")
				break
			}
			fmt.Fprintf(w, "%s %s %s\n", a.Kind, a.RelPath, a.Reason)
		}
	}
	for _, b := range r.Blockers {
		fmt.Fprintln(w, "阻止项:", b)
	}
}
