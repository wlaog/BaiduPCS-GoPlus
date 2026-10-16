package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
)

func partialFixture(t *testing.T) (*Plan, *cleanupWriter) {
	t.Helper()
	root := t.TempDir()
	writeLocal(t, root, "good/keep.txt|k", "bad/a.txt|a", "bad/b.txt|b")
	fs := newFakeFS().addTree("/backup", withHash("good/keep.txt", "k"), withHash("bad/a.txt", "a"), withHash("bad/b.txt", "b"), "old/deep/file.txt|4", withHash("good/deleted.txt", "x"))
	fs.failList["/backup/bad"] = &RemoteError{ErrType: pcserror.ErrTypeRemoteError, Code: 600, Msg: "unknown"}
	p, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil {
		t.Fatal(err)
	}
	return p, newCleanupWriter(fs)
}

func TestCloudError600ProtectsBranchAndContinuesComparisonAndRemoval(t *testing.T) {
	p, w := partialFixture(t)
	if p.ScanComplete || !p.PartialScanSafe || !p.CanApply() || len(p.Unverified) != 1 || len(p.Pending) != 0 || len(p.Keeps) != 1 || len(p.Removes) != 2 {
		t.Fatalf("bad directory must not become WAIT or block successful comparisons: %+v", p)
	}
	if w.listCalls["/backup/good"] != 1 {
		t.Fatal("successful sibling was not scanned")
	}
	if !runCleanup(t, p, w).Succeeded() || len(w.removes) != 2 {
		t.Fatal("verified independent items must remain executable")
	}
	if _, err := w.Meta("/backup/bad/a.txt"); err != nil {
		t.Fatal("unread branch was touched")
	}
	remaining := RemainingPlan(p, &ApplyReport{Removes: []ApplyResult{{Action: p.Removes[0], OK: true}, {Action: p.Removes[1], OK: true}}})
	if remaining == nil || remaining.Changes() != 0 || len(remaining.ScanIssues) != 1 {
		t.Fatal("completed cleanup must retain failed-scope scan state")
	}
}

func TestFailedScopeRescanReusesSuccessAndDoesNotReplayCompletedActions(t *testing.T) {
	p, w := partialFixture(t)
	rep := runCleanup(t, p, w)
	remaining := RemainingPlan(p, rep)
	delete(w.failList, "/backup/bad")
	w.listCalls = make(map[string]int)
	retry, err := RetryFailedScan(w, remaining, nil)
	if err != nil || !retry.ScanComplete || len(retry.ScanIssues) != 0 || len(retry.Keeps) != 3 || retry.Changes() != 0 || len(retry.Pending) != 0 {
		t.Fatalf("rescan must merge resolved scope without restoring completed actions: %v %+v", err, retry)
	}
	if len(w.listCalls) != 1 || w.listCalls["/backup/bad"] != 1 || retry.Performance.CloudListings != 1 || retry.Performance.HashBytes != 2 {
		t.Fatalf("only failed scope and previously unread contents should be read: %+v %+v", w.listCalls, retry.Performance)
	}
}

func TestStillFailedRescanKeepsProtectedScopeAndHealthyActions(t *testing.T) {
	p, w := partialFixture(t)
	w.listCalls = make(map[string]int)
	retry, err := RetryFailedScan(w, p, nil)
	if err != nil || retry.CanApply() == false || len(retry.ScanIssues) != 1 || len(retry.Pending) != 0 || len(retry.Removes) != 2 || len(w.listCalls) != 1 {
		t.Fatal("failed rescan must retain independent actions and must not become a full scan")
	}
}

func TestExecutorRejectsActionsOverlappingUnverifiedScopes(t *testing.T) {
	p, w := partialFixture(t)
	p.Removes = append(p.Removes, PlannedAction{Kind: ActionRemove, RelPath: "bad", RemotePath: "/backup/bad", IsDir: true, WholeDir: true}, PlannedAction{Kind: ActionRemove, RelPath: "bad/a.txt", RemotePath: "/backup/bad/a.txt"})
	rep := runCleanup(t, p, w)
	if rep.Failures != 2 || len(w.removes) != 2 {
		t.Fatal("executor must independently enforce scope protection")
	}
}

func TestLocalReadAndStatErrorsContinueAndProtectVersions(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			root := t.TempDir()
			writeLocal(t, root, "bad/a.txt|a", "good/keep.txt|k")
			opt := LocalScanOptions{}
			if directory {
				opt.readDir = func(p string) ([]os.DirEntry, error) {
					if p == filepath.Join(root, "bad") {
						return nil, fmt.Errorf("read denied")
					}
					return os.ReadDir(p)
				}
			} else {
				opt.lstat = func(p string) (os.FileInfo, error) {
					if p == filepath.Join(root, "bad", "a.txt") {
						return nil, fmt.Errorf("stat denied")
					}
					return os.Lstat(p)
				}
			}
			local, err := ScanLocal(root, opt)
			if err != nil || len(local.Errors) != 1 || local.Entries["good/keep.txt"].Size != 1 {
				t.Fatal("local failure must not discard later branches")
			}
			fs := newFakeFS().addTree("/backup", withHash("bad/a.txt", "old"), withHash("bad/a(1).txt", "a"), withHash("good/keep.txt", "k"), withHash("good/deleted.txt", "x"))
			cloud, err := ScanCloud(fs, "/backup", ScanLimits{})
			if err != nil {
				t.Fatal(err)
			}
			p := assemblePlan(local, cloud, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"}, nil)
			if p.LocalScanComplete || !p.CanApply() || len(p.Removes) != 1 || len(p.Renames) != 0 || len(p.Pending) != 0 {
				t.Fatal("unread local scope and candidate history must be protected")
			}
			w := newCleanupWriter(fs)
			if !runCleanup(t, p, w).Succeeded() || len(w.removes) != 1 {
				t.Fatal("healthy removal should continue")
			}
			retry, err := RetryFailedScan(w, RemainingPlan(p, &ApplyReport{Removes: []ApplyResult{{Action: p.Removes[0], OK: true}}}), nil)
			if err != nil || !retry.LocalScanComplete || len(retry.ScanIssues) != 0 || len(retry.Renames) != 1 {
				t.Fatalf("failed local scope should be reread alone and find matching version: %v %+v", err, retry)
			}
		})
	}
}

func TestRootListingFailureStillBlocksUntilRootCanBeVerified(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, "keep.txt|k")
	fs := newFakeFS().addTree("/backup", "old.txt|1")
	fs.failList["/backup"] = fmt.Errorf("root unavailable")
	p, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil || p.CanApply() || len(BuildPlanReport(p, "preview").Blockers) == 0 {
		t.Fatal("unscoped root failure cannot authorize removals")
	}
	delete(fs.failList, "/backup")
	retry, err := RetryFailedScan(fs, p, nil)
	if err != nil || !retry.CanApply() || len(retry.Removes) != 1 {
		t.Fatal("root retry should retain local scan and recover")
	}
}

func TestSuccessfulRenameIsNotRestoredByFailedScopeRescan(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, "good/a.txt|new", "bad/b.txt|b")
	fs := newFakeFS().addTree("/backup", withHash("good/a.txt", "old"), withHash("good/a(1).txt", "new"), withHash("good/a(2).txt", "older"), withHash("bad/b.txt", "b"))
	fs.failList["/backup/bad"] = fmt.Errorf("temporary read failure")
	p, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil {
		t.Fatal(err)
	}
	w := newCleanupWriter(fs)
	rep := runCleanup(t, p, w)
	if !rep.Succeeded() || len(w.moves) != 1 {
		t.Fatal("healthy rename must complete despite unrelated scan gap")
	}
	delete(w.failList, "/backup/bad")
	retry, err := RetryFailedScan(w, RemainingPlan(p, rep), nil)
	if err != nil || retry.Changes() != 0 || len(retry.Keeps) != 2 {
		t.Fatalf("rescan must preserve verified rename target and removed source/history: %v %+v", err, retry)
	}
}
