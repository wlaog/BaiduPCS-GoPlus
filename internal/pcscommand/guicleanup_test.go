package pcscommand

import (
	"encoding/json"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuiKeepsOnlyFailedActionsForRetryAndPageReload(t *testing.T) {
	s := &GuiSession{}
	first := reconcile.PlannedAction{Kind: reconcile.ActionRemove, RelPath: "done.txt", RemotePath: "/backup/done.txt"}
	second := reconcile.PlannedAction{Kind: reconcile.ActionRemove, RelPath: "retry.txt", RemotePath: "/backup/retry.txt"}
	plan := &reconcile.Plan{LocalRoot: t.TempDir(), RemoteRoot: "/backup", LocalFiles: 1, ScanComplete: true, LocalScanComplete: true, Removes: []reconcile.PlannedAction{first, second}}
	report := &reconcile.ApplyReport{Removes: []reconcile.ApplyResult{{Action: first, OK: true}, {Action: second, Error: "busy"}}, Failures: 1}
	preview := s.finishApply(plan, report)
	if preview == nil || s.plan == nil || s.plan.Changes() != 1 || preview.Removes[0].RelPath != "retry.txt" {
		t.Fatal("must retain only unfinished actions")
	}
	if s.progress.Status != "warning" {
		t.Fatal("partial completion must not show successful 100 percent")
	}
	rec := httptest.NewRecorder()
	s.handleState(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var payload struct {
		PendingPlan *reconcile.PlanReport `json:"pendingPlan"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PendingPlan == nil || payload.PendingPlan.Summary.Changes != 1 {
		t.Fatal("page reload must be able to recover the remaining preview")
	}
	done := &reconcile.ApplyReport{Removes: []reconcile.ApplyResult{{Action: second, OK: true}}}
	if s.finishApply(s.plan, done) != nil || s.plan != nil {
		t.Fatal("completed actions must not remain retryable")
	}
}

func TestGuiRetainsScanGapsAfterAllExecutableItemsComplete(t *testing.T) {
	s := &GuiSession{}
	a := reconcile.PlannedAction{Kind: reconcile.ActionRemove, RelPath: "old.txt", RemotePath: "/backup/old.txt"}
	p := &reconcile.Plan{LocalRoot: t.TempDir(), RemoteRoot: "/backup", LocalFiles: 1, LocalScanComplete: true, PartialScanSafe: true, ScanIssues: []reconcile.ScanIssue{{Side: "云端", RelPath: "bad", Reason: "600"}}, Removes: []reconcile.PlannedAction{a}}
	report := &reconcile.ApplyReport{Removes: []reconcile.ApplyResult{{Action: a, OK: true}}}
	preview := s.finishApply(p, report)
	if preview == nil || preview.Summary.Changes != 0 || len(preview.ScanIssues) != 1 || s.progress.Status != "warning" {
		t.Fatal("completed operations must preserve unresolved scan scopes and show a warning")
	}
}
