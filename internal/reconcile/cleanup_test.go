package reconcile

import (
	"encoding/json"
	"fmt"
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This writer has no upload, mkdir or copy methods. Tests exercise the actual
// reduced mutation surface, including partial failure and ambiguous replies.
type cleanupWriter struct {
	*fakeFS
	moves                [][2]string
	removes              []string
	failMove, failRemove map[string]error
	busy                 map[string]int
	moveCalls            int
	lostMoveReply        bool
	metaHook             func(string)
}

func newCleanupWriter(fs *fakeFS) *cleanupWriter {
	return &cleanupWriter{fakeFS: fs, failMove: map[string]error{}, failRemove: map[string]error{}, busy: map[string]int{}}
}
func (w *cleanupWriter) Meta(p string) (RemoteEntry, error) {
	if w.metaHook != nil {
		w.metaHook(p)
	}
	return w.fakeFS.Meta(p)
}
func (w *cleanupWriter) Remove(paths ...string) error {
	for _, p := range paths {
		if err := w.failRemove[p]; err != nil {
			return err
		}
		w.removes = append(w.removes, p)
		for abs := range w.metas {
			if abs == p || strings.HasPrefix(abs, p+"/") {
				delete(w.metas, abs)
				delete(w.dirs, abs)
			}
		}
		for dir, children := range w.dirs {
			var kept []RemoteEntry
			for _, e := range children {
				if e.Path != p && !strings.HasPrefix(e.Path, p+"/") {
					kept = append(kept, e)
				}
			}
			w.dirs[dir] = kept
		}
	}
	return nil
}
func (w *cleanupWriter) Move(from, to string) error {
	w.moveCalls++
	if w.busy[from] > 0 {
		w.busy[from]--
		return &RemoteError{ErrType: pcserror.ErrTypeRemoteError, Code: 31171, Msg: "other async job is doing now"}
	}
	if err := w.failMove[from]; err != nil {
		return err
	}
	e, ok := w.metas[from]
	if !ok {
		return &NotFoundError{Path: from}
	}
	if _, exists := w.metas[to]; exists {
		return fmt.Errorf("target exists")
	}
	delete(w.metas, from)
	parent := ParentRemote(from)
	var kept []RemoteEntry
	for _, child := range w.dirs[parent] {
		if child.Path != from {
			kept = append(kept, child)
		}
	}
	e.Path = to
	e.Name = baseName(to)
	w.metas[to] = e
	w.dirs[parent] = append(kept, e)
	w.moves = append(w.moves, [2]string{from, to})
	if w.lostMoveReply {
		w.lostMoveReply = false
		return fmt.Errorf("reply lost after successful move")
	}
	return nil
}
func permanentFailure() error {
	return &RemoteError{ErrType: pcserror.ErrTypeRemoteError, Code: 31061, Msg: "request refused"}
}
func cleanupOptions() ApplyOptions {
	return ApplyOptions{RemovalMode: RemovalRecycle, ConfirmRemoval: true, AcknowledgeRecycle: true, AcknowledgeBlast: true}
}
func cleanupPlan(t *testing.T, localSpec, cloudSpec []string) (*Plan, *cleanupWriter) {
	t.Helper()
	root := t.TempDir()
	writeLocal(t, root, localSpec...)
	fs := newFakeFS().addTree("/backup", cloudSpec...)
	plan, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil {
		t.Fatal(err)
	}
	return plan, newCleanupWriter(fs)
}
func withHash(name, content string) string {
	return fmt.Sprintf("%s|%d|%s", name, len(content), md5hex([]byte(content)))
}
func runCleanup(t *testing.T, p *Plan, w *cleanupWriter) *ApplyReport {
	t.Helper()
	e := Executor{Writer: w, Sleep: noSleep}
	rep, err := e.Apply(p, cleanupOptions())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestCleanupOnlyRenamesMatchingContentAndRemovesOldCopies(t *testing.T) {
	p, w := cleanupPlan(t, []string{"paper.txt|new", "newdir/file.txt|waiting"}, []string{withHash("paper.txt", "old"), withHash("paper(1).txt", "new"), withHash("paper(2).txt", "older"), withHash("deleted.txt", "gone")})
	if len(p.Renames) != 1 || !strings.HasSuffix(p.Renames[0].SourcePath, "paper(1).txt") || len(p.Pending) != 2 || len(p.Uploads)+len(p.Replaces)+len(p.Mkdirs) != 0 {
		t.Fatalf("unexpected cleanup plan: %+v", p)
	}
	rep := runCleanup(t, p, w)
	if !rep.Succeeded() || len(w.moves) != 1 || len(w.removes) != 3 {
		t.Fatalf("unexpected results: %+v", rep)
	}
	current, _ := w.Meta("/backup/paper.txt")
	if current.ContentMD5 != md5hex([]byte("new")) {
		t.Fatal("wrong version selected")
	}
	if len(p.Pending) == 0 {
		t.Fatal("missing files must wait for official backup")
	}
	b, _ := json.Marshal(BuildPlanReport(p, "preview"))
	if strings.Contains(string(b), `"uploads"`) || strings.Contains(string(b), `"replaces"`) {
		t.Fatal("new preview must expose cleanup actions only")
	}
}

func TestWaitingForOfficialBackupProtectsItsHistoryOnly(t *testing.T) {
	p, w := cleanupPlan(t, []string{"a.txt|new"}, []string{withHash("a.txt", "old"), withHash("a(2).txt", "old"), withHash("deleted.txt", "x")})
	if len(p.Pending) != 1 || len(p.Renames) != 0 || len(p.Removes) != 1 || p.Removes[0].RelPath != "deleted.txt" {
		t.Fatal("unconfirmed versions must remain protected")
	}
	rep := runCleanup(t, p, w)
	if !rep.Succeeded() {
		t.Fatal("unrelated cleanup should succeed")
	}
	if _, err := w.Meta("/backup/a(2).txt"); err != nil {
		t.Fatal("history removed before latest exists")
	}
}

func TestLocallyOwnedSuffixAndNestedSuffixArePreserved(t *testing.T) {
	for _, suffix := range []string{"(2)", "（2）", " (2)"} {
		t.Run(suffix, func(t *testing.T) {
			p, _ := cleanupPlan(t, []string{"a.txt|new", "a(1).txt|own"}, []string{withHash("a.txt", "old"), withHash("a(1).txt", "own"), withHash("a(1)"+suffix+".txt", "own")})
			if len(p.Pending) != 1 || len(p.Keeps) != 1 || len(p.Renames) != 0 || len(p.Removes) != 1 || p.Removes[0].Requires != "/backup/a(1).txt" {
				t.Fatalf("real suffix file must own its versions: %+v", p)
			}
		})
	}
}

func TestFailedRenamePreservesDependentCopiesAndRetrySkipsFullScan(t *testing.T) {
	p, w := cleanupPlan(t, []string{"a.txt|new"}, []string{withHash("a.txt", "old"), withHash("a(1).txt", "new"), withHash("a(2).txt", "old"), withHash("deleted.txt", "x")})
	w.failMove["/backup/a(1).txt"] = permanentFailure()
	rep := runCleanup(t, p, w)
	if rep.Succeeded() || !rep.Renames[0].TargetRemoved {
		t.Fatal("rename must retain its partial state")
	}
	if _, err := w.Meta("/backup/a(2).txt"); err != nil {
		t.Fatal("dependent history must survive failed rename")
	}
	if _, err := w.Meta("/backup/deleted.txt"); !IsNotFound(err) {
		t.Fatal("unrelated deletion should continue")
	}
	remaining := RemainingPlan(p, rep)
	if remaining == nil || len(remaining.Renames) != 1 || len(remaining.Removes) != 1 {
		t.Fatal("only unfinished items should remain")
	}
	w.failMove = map[string]error{}
	w.listCalls = map[string]int{}
	retry := runCleanup(t, remaining, w)
	if !retry.Succeeded() || RemainingPlan(remaining, retry) != nil || len(w.moves) != 1 {
		t.Fatal("retry must complete once without replaying successful actions")
	}
	if len(w.listCalls) != 0 {
		t.Fatal("retrying files must not rescan cloud directories")
	}
}

func TestBusyAndLostMoveReplyAreRetriedWithStateChecks(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			p, w := cleanupPlan(t, []string{"a.txt|new"}, []string{withHash("a.txt", "old"), withHash("a(1).txt", "new")})
			if lost {
				w.lostMoveReply = true
			} else {
				w.busy["/backup/a(1).txt"] = 2
			}
			rep := runCleanup(t, p, w)
			if !rep.Succeeded() || len(w.moves) != 1 || len(w.removes) != 1 {
				t.Fatalf("retry must converge without removing the new target: %+v", rep)
			}
		})
	}
}

func TestRemovalFailuresDoNotStopIndependentItems(t *testing.T) {
	p, w := cleanupPlan(t, []string{"keep.txt|k"}, []string{withHash("keep.txt", "k"), withHash("a.txt", "a"), withHash("b.txt", "b"), withHash("c.txt", "c")})
	w.failRemove["/backup/b.txt"] = permanentFailure()
	rep := runCleanup(t, p, w)
	if rep.Failures != 1 || len(w.removes) != 2 || len(rep.Removes) != 3 {
		t.Fatal("one removal failure must not stop the rest")
	}
	remaining := RemainingPlan(p, rep)
	if remaining == nil || remaining.Changes() != 1 || remaining.Removes[0].RelPath != "b.txt" {
		t.Fatal("retry plan must only retain failed removal")
	}
}

func TestNewLocalOrCloudChangesSkipOnlyAffectedItem(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			cloud := []string{withHash("keep.txt", "k"), withHash("deleted.txt", "x")}
			if directory {
				cloud = append(cloud, withHash("old/a.txt", "x"))
			} else {
				cloud = append(cloud, withHash("b.txt", "x"))
			}
			p, w := cleanupPlan(t, []string{"keep.txt|k"}, cloud)
			if directory {
				e := w.metas["/backup/old"]
				e.Mtime++
				w.metas[e.Path] = e
			} else {
				w.metaHook = func(path string) {
					if path == "/backup/b.txt" {
						_ = os.WriteFile(filepath.Join(p.LocalRoot, "b.txt"), []byte("new local"), 0600)
					}
				}
			}
			rep := runCleanup(t, p, w)
			if rep.Failures != 1 {
				t.Fatalf("expected exactly one changed item, got %+v", rep)
			}
			if _, err := w.Meta("/backup/deleted.txt"); !IsNotFound(err) {
				t.Fatal("unrelated cleanup did not continue")
			}
		})
	}
}

func TestTypeConflictDoesNotDeleteChildrenOrBlockOtherItems(t *testing.T) {
	p, w := cleanupPlan(t, []string{"thing|local"}, []string{withHash("thing/child.txt", "remote"), withHash("deleted.txt", "x")})
	if len(p.Conflicts) != 1 || len(p.Removes) != 1 {
		t.Fatal("conflicting subtree must stay intact")
	}
	if len(BuildPlanReport(p, "preview").Blockers) != 0 {
		t.Fatal("a single conflict must not block independent cleanup")
	}
	if !runCleanup(t, p, w).Succeeded() {
		t.Fatal("unrelated action should complete")
	}
	if _, err := w.Meta("/backup/thing/child.txt"); err != nil {
		t.Fatal("conflicting subtree was damaged")
	}
}

func TestCleanupConfirmationThresholdAndIncompleteScan(t *testing.T) {
	for _, kind := range []string{"confirmation", "threshold", "scan", "empty", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			p, w := cleanupPlan(t, []string{"a.txt|new"}, []string{withHash("a.txt", "old"), withHash("a(1).txt", "new"), withHash("other.txt", "x")})
			opt := cleanupOptions()
			switch kind {
			case "confirmation":
				opt.ConfirmRemoval = false
			case "threshold":
				opt.AcknowledgeBlast = false
				opt.MaxRemovePercent = 1
			case "scan":
				p.ScanComplete = false
			case "empty":
				p.LocalFiles = 0
				p.LocalDirs = 0
			case "legacy":
				p.Uploads = []PlannedAction{{Kind: ActionUpload}}
			}
			e := Executor{Writer: w, Sleep: noSleep}
			_, _ = e.Apply(p, opt)
			if len(w.moves)+len(w.removes) > 0 {
				t.Fatal("global preconditions must stop every mutation")
			}
		})
	}
}

func TestCleanupReportsEmptyArraysAndAccuratePartialSuccess(t *testing.T) {
	r := &ApplyReport{}
	b, err := json.Marshal(r.ToJSON())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	for _, key := range []string{"renames", "removes"} {
		if string(fields[key]) != "[]" {
			t.Fatal("empty actions must be JSON arrays")
		}
	}
	r.Removes = []ApplyResult{{Skipped: true}}
	if r.Succeeded() {
		t.Fatal("skipped operations must not be reported as complete")
	}
}

func TestMissingLocalRootCannotCauseCloudDeletion(t *testing.T) {
	p, w := cleanupPlan(t, []string{"keep.txt|k"}, []string{withHash("keep.txt", "k"), withHash("old.txt", "x")})
	p.LocalRoot = filepath.Join(p.LocalRoot, "missing-root")
	e := Executor{Writer: w, Sleep: noSleep}
	_, err := e.Apply(p, cleanupOptions())
	if err == nil || len(w.removes) > 0 {
		t.Fatal("unavailable local root must never look like an empty tree")
	}
}

func TestExcludedVersionCannotBeRenamed(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, "a.txt|new")
	fs := newFakeFS().addTree("/backup", withHash("a.txt", "old"), withHash("a(1).txt", "new"))
	p, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup", SkipNames: []string{"a(1).txt"}})
	if err != nil || len(p.Renames) != 0 || len(p.Removes) != 0 || len(p.Pending) != 1 {
		t.Fatal("excluded cloud files must remain untouched even if content matches")
	}
}

func TestOfficialClientFinishingTargetProducesSmallRetryPlan(t *testing.T) {
	p, w := cleanupPlan(t, []string{"a.txt|new"}, []string{withHash("a.txt", "old"), withHash("a(1).txt", "new")})
	e := w.metas["/backup/a.txt"]
	e.ContentMD5 = md5hex([]byte("new"))
	w.metas[e.Path] = e
	rep := runCleanup(t, p, w)
	if len(w.moves)+len(w.removes) != 0 || !rep.Renames[0].DeferredSource {
		t.Fatal("an already updated target must not be overwritten")
	}
	remaining := RemainingPlan(p, rep)
	if remaining == nil || len(remaining.Renames) != 0 || len(remaining.Removes) != 1 || remaining.Removes[0].RemotePath != "/backup/a(1).txt" {
		t.Fatal("only the redundant source should remain for retry")
	}
	if !runCleanup(t, remaining, w).Succeeded() {
		t.Fatal("source cleanup should finish without another tree scan")
	}
}
