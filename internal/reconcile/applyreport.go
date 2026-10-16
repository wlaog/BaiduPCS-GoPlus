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

// ApplyReportJSON is the serialisable form of an apply run.
type ApplyReportJSON struct {
	ApplyVersion string `json:"applyVersion"`
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt"`
	LocalRoot    string `json:"localRoot"`
	RemoteRoot   string `json:"remoteRoot"`
	RemovalMode  string `json:"removalMode"`
	Quarantine   string `json:"quarantineRoot"`
	RunID        string `json:"runId"`
	Succeeded    bool   `json:"succeeded"`

	Mkdirs   []ApplyResult `json:"mkdirs"`
	Uploads  []ApplyResult `json:"uploads"`
	Verifies []ApplyResult `json:"verifies"`
	Removes  []ApplyResult `json:"removes"`

	UploadFailures int      `json:"uploadFailures"`
	RemovalSkipped bool     `json:"removalSkipped"`
	RemovalReason  string   `json:"removalReason,omitempty"`
	Aborted        bool     `json:"aborted"`
	AbortReason    string   `json:"abortReason,omitempty"`
	RecoverySteps  []string `json:"recoverySteps,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// ToJSON converts an apply report for serialisation.
func (r *ApplyReport) ToJSON() *ApplyReportJSON {
	return &ApplyReportJSON{
		ApplyVersion:   r.ApplyVersion,
		StartedAt:      r.StartedAt,
		FinishedAt:     r.FinishedAt,
		LocalRoot:      r.LocalRoot,
		RemoteRoot:     r.RemoteRoot,
		RemovalMode:    r.RemovalMode,
		Quarantine:     r.Quarantine,
		RunID:          r.RunID,
		Succeeded:      r.Succeeded(),
		Mkdirs:         append([]ApplyResult{}, r.Mkdirs...),
		Uploads:        append([]ApplyResult{}, r.Uploads...),
		Verifies:       append([]ApplyResult{}, r.Verifies...),
		Removes:        append([]ApplyResult{}, r.Removes...),
		UploadFailures: r.UploadFailures,
		RemovalSkipped: r.RemovalSkipped,
		RemovalReason:  r.RemovalReason,
		Aborted:        r.Aborted,
		AbortReason:    r.AbortReason,
		RecoverySteps:  r.RecoverySteps,
		Warnings:       r.Warnings,
	}
}

// WriteApplyReport saves the run record and returns its path.
func WriteApplyReport(dir string, r *ApplyReport) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("apply-%s.json", time.Now().Format("20060102-150405"))
	full := filepath.Join(dir, name)
	data, err := json.MarshalIndent(r.ToJSON(), "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return full, nil
}

// RenderApplyHuman writes the operator-facing run summary.
func RenderApplyHuman(w io.Writer, r *ApplyReport) {
	p := func(format string, a ...interface{}) { fmt.Fprintf(w, format+"\n", a...) }

	p("=== 对齐本地 · 执行结果 ===")
	p("开始: %s   结束: %s", r.StartedAt, r.FinishedAt)
	p("本地(权威源): %s", r.LocalRoot)
	p("云端(已对齐): %s", r.RemoteRoot)
	p("清理方式: %s", r.RemovalMode)
	if r.RemovalMode == string(RemovalQuarantine) {
		p("隔离区: %s/%s", r.Quarantine, r.RunID)
	}
	p("")

	okCount, skipCount, failCount := tally(r.Mkdirs)
	p("--- 目录 ---")
	p("  创建成功 %d / 跳过 %d / 失败 %d", okCount, skipCount, failCount)

	uOK, uSkip, uFail := tally(r.Uploads)
	var uploaded int64
	for _, res := range r.Uploads {
		if res.OK {
			uploaded += res.Action.Size
		}
	}
	p("--- 上传/覆盖 ---")
	p("  成功 %d (%s) / 跳过 %d / 失败 %d", uOK, converter.ConvertFileSize(uploaded, 2), uSkip, uFail)

	if len(r.Verifies) > 0 {
		vOK, _, vFail := tally(r.Verifies)
		p("--- 写入后校验 ---")
		p("  云端已确认 %d / 不符 %d", vOK, vFail)
	}

	rOK, rSkip, rFail := tally(r.Removes)
	p("--- 清理 ---")
	if r.RemovalSkipped {
		p("  未执行: %s", r.RemovalReason)
	} else {
		p("  成功 %d / 跳过 %d / 失败 %d", rOK, rSkip, rFail)
	}
	p("")

	// Failures and skips are the actionable part; show them explicitly.
	showProblems := func(title string, sets ...[]ApplyResult) {
		var lines []string
		for _, set := range sets {
			for _, res := range set {
				if res.OK {
					continue
				}
				what := res.Action.RelPath
				if what == "" {
					what = res.Action.RemotePath
				}
				detail := res.Error
				if detail == "" {
					detail = res.Detail
				}
				lines = append(lines, fmt.Sprintf("  %-46s %s", truncate(what, 46), detail))
			}
		}
		if len(lines) == 0 {
			return
		}
		p("--- %s (%d) ---", title, len(lines))
		for _, l := range lines {
			p("%s", l)
		}
		p("")
	}
	showProblems("未完成/跳过/失败", r.Mkdirs, r.Uploads, r.Removes)

	if len(r.Warnings) > 0 {
		p("--- 警告 ---")
		for _, s := range r.Warnings {
			p("  %s", s)
		}
		p("")
	}
	if r.Aborted {
		p("--- 已中止 ---")
		p("  原因: %s", r.AbortReason)
		p("  清理未完成; 云端处于中间状态, 重新运行 apply 是幂等的, 会接着完成")
		p("")
	}
	if len(r.RecoverySteps) > 0 {
		p("--- 恢复 / 后续 ---")
		for _, s := range r.RecoverySteps {
			p("  %s", s)
		}
		p("")
	}

	switch {
	case r.Succeeded():
		p("结果: ✅ 完成, 云端已与本地一致。")
	case r.Aborted:
		p("结果: ⚠ 中途停止并已报告原因, 未做不安全的清理。")
	case r.RemovalSkipped:
		p("结果: ⚠ 上传已完成, 清理按安全规则未执行。")
	default:
		p("结果: ⚠ 存在未完成项, 见上方清单。")
	}
}

func tally(sets []ApplyResult) (ok, skipped, failed int) {
	for _, res := range sets {
		switch {
		case res.OK:
			ok++
		case res.Skipped:
			skipped++
		default:
			failed++
		}
	}
	return
}
