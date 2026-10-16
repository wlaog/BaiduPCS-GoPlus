package reconcile

import (
	"fmt"
	"io"
)

type ApplyReportJSON struct {
	ApplyVersion    string        `json:"applyVersion"`
	StartedAt       string        `json:"startedAt"`
	FinishedAt      string        `json:"finishedAt"`
	LocalRoot       string        `json:"localRoot"`
	RemoteRoot      string        `json:"remoteRoot"`
	RemovalMode     string        `json:"removalMode"`
	Succeeded       bool          `json:"succeeded"`
	Renames         []ApplyResult `json:"renames"`
	Removes         []ApplyResult `json:"removes"`
	Failures        int           `json:"failures"`
	PendingCount    int           `json:"pendingCount"`
	ConflictCount   int           `json:"conflictCount"`
	UnverifiedCount int           `json:"unverifiedCount"`
	RemovalSkipped  bool          `json:"removalSkipped"`
	RemovalReason   string        `json:"removalReason,omitempty"`
	Aborted         bool          `json:"aborted"`
	AbortReason     string        `json:"abortReason,omitempty"`
	RecoverySteps   []string      `json:"recoverySteps,omitempty"`
	Warnings        []string      `json:"warnings,omitempty"`
}

func (r *ApplyReport) ToJSON() *ApplyReportJSON {
	return &ApplyReportJSON{ApplyVersion: r.ApplyVersion, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, LocalRoot: r.LocalRoot, RemoteRoot: r.RemoteRoot, RemovalMode: r.RemovalMode, Succeeded: r.Succeeded(), Renames: append([]ApplyResult{}, r.Renames...), Removes: append([]ApplyResult{}, r.Removes...), Failures: r.Failures, PendingCount: r.PendingCount, ConflictCount: r.ConflictCount, UnverifiedCount: r.UnverifiedCount, RemovalSkipped: r.RemovalSkipped, RemovalReason: r.RemovalReason, Aborted: r.Aborted, AbortReason: r.AbortReason, RecoverySteps: r.RecoverySteps, Warnings: r.Warnings}
}
func WriteApplyReport(dir string, r *ApplyReport) (string, error) {
	return saveReport(dir, "apply", r.ToJSON())
}
func RenderApplyHuman(w io.Writer, r *ApplyReport) {
	fmt.Fprintf(w, "=== 云端整理 · 执行结果 ===\n开始: %s\n结束: %s\n", r.StartedAt, r.FinishedAt)
	ok, skip, fail := tally(r.Renames)
	fmt.Fprintf(w, "重命名: 成功 %d / 跳过 %d / 失败 %d\n", ok, skip, fail)
	ok, skip, fail = tally(r.Removes)
	fmt.Fprintf(w, "清理: 成功 %d / 跳过 %d / 失败 %d\n", ok, skip, fail)
	for _, set := range [][]ApplyResult{r.Renames, r.Removes} {
		for _, res := range set {
			if !res.OK {
				fmt.Fprintf(w, "未完成 %s: %s\n", res.Action.RelPath, res.Error)
			}
		}
	}
	if r.RemovalSkipped {
		fmt.Fprintln(w, r.RemovalReason)
	}
	if r.Aborted {
		fmt.Fprintln(w, r.AbortReason)
	}
	fmt.Fprintf(w, "等待官方备份 %d 项；需判断 %d 项。可执行项全部完成: %t\n", r.PendingCount, r.ConflictCount, r.Succeeded())
	if r.UnverifiedCount > 0 {
		fmt.Fprintf(w, "仍有 %d 个未核实范围已保护，可单独补扫\n", r.UnverifiedCount)
	}
}
func tally(set []ApplyResult) (ok, skip, fail int) {
	for _, r := range set {
		if r.OK {
			ok++
		} else if r.Skipped {
			skip++
		} else {
			fail++
		}
	}
	return
}
