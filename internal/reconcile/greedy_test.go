package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestGreedyDirectoryRemovalNeverListsOrDeletesDescendants(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, "keep.txt|k")
	fs := newFakeFS().addTree("/backup", withHash("keep.txt", "k"))
	for i := 0; i < 100; i++ {
		fs.addPath("/backup", fmt.Sprintf("old/branch%d/deep/file.txt|1024", i))
	}
	fs.addPath("/backup", "old/desktop.ini|12")
	fs.addPath("/backup", "old/internal.tmp|8")
	full, err := ScanCloud(fs, "/backup", ScanLimits{})
	if err != nil || full.Stats.ListingRequests != 202 {
		t.Fatal("fixture should require 202 directory listings for a full scan")
	}
	fs.listCalls = make(map[string]int)
	// A listing failure inside a deliberately opaque subtree is irrelevant:
	// no request should even be made there.
	fs.failList["/backup/old"] = fmt.Errorf("must not be listed")
	p, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil || !p.ScanComplete || len(p.Removes) != 1 || !p.Removes[0].WholeDir || p.Removes[0].RelPath != "old" {
		t.Fatalf("expected one complete opaque directory action: %v, %+v", err, p)
	}
	if p.Performance.CloudListings != 1 || p.Performance.PrunedDirs != 1 || len(fs.listCalls) != 1 {
		t.Fatal("only the backup root should have been listed")
	}
	r := BuildPlanReport(p, "preview")
	if r.Summary.UnscannedRemoveDirs != 1 || !r.Removes[0].WholeDir || r.Summary.RemoveFiles != 0 || r.Summary.RemoveBytes != 0 {
		t.Fatal("unknown directory contents must be explicitly separate from measured counts")
	}
	w := newCleanupWriter(fs)
	if !runCleanup(t, p, w).Succeeded() || len(w.removes) != 1 || w.removes[0] != "/backup/old" || len(fs.listCalls) != 1 {
		t.Fatal("execution must remove the parent once without scanning or deleting children")
	}
	for path := range w.metas {
		if IsSubPath("/backup/old", path) {
			t.Fatalf("directory or descendant was left behind: %s", path)
		}
	}
}

func TestGreedyChoosesFirstAbsentDirectoryAndKeepsLocalAncestors(t *testing.T) {
	p, w := cleanupPlan(t, []string{"live/deep/keep.txt|k"}, []string{
		withHash("live/deep/keep.txt", "k"), "live/old/nested/file.txt|2", "live(1)/sub/file.txt|3", "empty/",
	})
	for _, rel := range []string{"live/old", "live(1)", "empty"} {
		if !hasRel(p.Removes, rel) {
			t.Fatalf("missing greedy directory action %s", rel)
		}
	}
	if len(p.Removes) != 3 || w.listCalls["/backup/live/old"] != 0 || w.listCalls["/backup/live(1)"] != 0 {
		t.Fatal("only highest absent directories should be actions; matching names elsewhere are irrelevant")
	}
	if !runCleanup(t, p, w).Succeeded() {
		t.Fatal("directory removals should complete")
	}
	if _, err := w.Meta("/backup/live/deep/keep.txt"); err != nil {
		t.Fatal("local matching branch was removed")
	}
}

func TestGreedyProtectsExcludedDirectoryItself(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, "keep.txt|k")
	fs := newFakeFS().addTree("/backup", "cache/nested/file.txt|1", "other/file.txt|2")
	p, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup", SkipNames: []string{"cache"}})
	if err != nil || len(p.Removes) != 1 || p.Removes[0].RelPath != "other" || !p.Removes[0].WholeDir {
		t.Fatal("an explicitly excluded directory must remain protected")
	}
}

func TestGreedyDirectoryReappearingLocallySkipsOnlyThatAction(t *testing.T) {
	p, w := cleanupPlan(t, []string{"keep.txt|k"}, []string{"a/deep/file.txt|1", "b/deep/file.txt|2"})
	if err := os.Mkdir(filepath.Join(p.LocalRoot, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	rep := runCleanup(t, p, w)
	if rep.Failures != 1 || len(w.removes) != 1 || w.removes[0] != "/backup/b" || w.listCalls["/backup/a"] != 0 || w.listCalls["/backup/b"] != 0 {
		t.Fatal("reappeared local directory must be preserved while unrelated cleanup continues")
	}
}

func TestGreedyDirectoryFailureCanRetryWithoutListing(t *testing.T) {
	p, w := cleanupPlan(t, []string{"keep.txt|k"}, []string{"a/deep/file.txt|1", "b/deep/file.txt|2"})
	w.failRemove["/backup/a"] = permanentFailure()
	rep := runCleanup(t, p, w)
	remaining := RemainingPlan(p, rep)
	if rep.Failures != 1 || remaining == nil || remaining.Changes() != 1 || !remaining.Removes[0].WholeDir || len(w.removes) != 1 {
		t.Fatal("failed parent must remain one retryable unit and other folders must finish")
	}
	delete(w.failRemove, "/backup/a")
	if !runCleanup(t, remaining, w).Succeeded() || len(w.listCalls) != 1 || len(w.removes) != 2 {
		t.Fatal("retry must not enumerate descendants or replay successful folders")
	}
}

func TestGreedyUnknownCountsRequireScopeAcknowledgment(t *testing.T) {
	p, w := cleanupPlan(t, []string{"keep.txt|k"}, []string{"old/file.txt|1"})
	opt := cleanupOptions()
	opt.AcknowledgeBlast = false
	e := Executor{Writer: w, Sleep: noSleep}
	r, err := e.Apply(p, opt)
	if err != nil || !r.RemovalSkipped || len(w.removes) != 0 {
		t.Fatal("unknown contents must not silently bypass existing removal thresholds")
	}
}
