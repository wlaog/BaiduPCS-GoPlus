package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLocal creates a local file tree from spec lines: "rel" for a directory
// (trailing slash) or "rel|content".
func writeLocal(t *testing.T, root string, spec ...string) {
	t.Helper()
	for _, line := range spec {
		if strings.HasSuffix(line, "/") {
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(line, "/"))), 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		rel, content, _ := strings.Cut(line, "|")
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func planFor(t *testing.T, fs Reader, local string, opt PlanOptions) *Plan {
	t.Helper()
	opt.LocalRoot = local
	if opt.RemoteRoot == "" {
		opt.RemoteRoot = "/root"
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	// Disable the mid-write window by default: these fixtures write files
	// immediately before planning, and the point of most cases is the
	// comparison rule, not the editing guard. TestPlanFlagsLocallyEditingFile
	// sets a real window on purpose.
	if opt.EditingWindow == 0 {
		opt.EditingWindow = -1
	}
	p, err := BuildPlan(fs, opt)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return p
}

func kindsOf(actions []PlannedAction) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.RelPath)
	}
	return out
}

func hasRel(actions []PlannedAction, rel string) bool {
	for _, a := range actions {
		if a.RelPath == rel {
			return true
		}
	}
	return false
}

// The worked example from the project README, which is also the acceptance
// criteria: paper.docx is stale in the cloud, notes.md is missing, plot.png
// agrees, and the auto-generated paper(1).docx plus a deleted file are
// redundant cloud copies.
func TestLooksLikeMD5(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"781e5e245d69b566979b86e28d23f2c7", true},
		{"3f2c8bd9cfde6550fdff4b36617c3261", true},
		{"00000000000000000000000000000000", true},
		{"", false},
		{"too-short", false},
		{"781e5e245d69b566979b86e28d23f2c", false},   // 31 chars
		{"781e5e245d69b566979b86e28d23f2c77", false}, // 33 chars
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", false},
		{"781E5E245D69B566979B86E28D23F2C7", false}, // caller lowercases first
	}
	for _, c := range cases {
		if got := looksLikeMD5(c.in); got != c.want {
			t.Errorf("looksLikeMD5(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPlanTypeMismatchesAreConflictsNotDeletions(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "thing/", "thing/inner.txt|x", "plain.txt|y")

	fs := newFakeFS().addTree("/root",
		"thing|12",   // cloud has a FILE where local has a DIRECTORY
		"plain.txt/", // cloud has a DIRECTORY where local has a FILE
	)

	plan := planFor(t, fs, local, PlanOptions{})

	if !hasRel(plan.Conflicts, "thing") {
		t.Errorf("dir-vs-file must be CONFLICT, got %+v", kindsOf(plan.Conflicts))
	}
	if !hasRel(plan.Conflicts, "plain.txt") {
		t.Errorf("file-vs-dir must be CONFLICT, got %+v", kindsOf(plan.Conflicts))
	}
	for _, a := range plan.Removes {
		if a.RelPath == "thing" || a.RelPath == "plain.txt" {
			t.Errorf("a type mismatch must never become a removal: %+v", a)
		}
	}
}

func hasCloudDir(fs *fakeFS, path string) bool {
	e, ok := fs.metas[path]
	return ok && e.IsDir
}

// A cloud-only directory is removed as one unit, so its children must not be
// listed as separate removals (which would produce a redundant, confusing plan).
func TestPlanRemovesCloudOnlyDirectoryAsOneUnit(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "keep.txt|keep")

	fs := newFakeFS().addTree("/root",
		"keep.txt|4",
		"olddir/",
		"olddir/a.txt|1",
		"olddir/b.txt|2",
		"olddir/sub/",
		"olddir/sub/c.txt|3",
	)

	plan := planFor(t, fs, local, PlanOptions{})

	if !hasRel(plan.Removes, "olddir") {
		t.Errorf("olddir must be removed as a unit, got %v", kindsOf(plan.Removes))
	}
	for _, child := range []string{"olddir/a.txt", "olddir/b.txt", "olddir/sub", "olddir/sub/c.txt"} {
		if hasRel(plan.Removes, child) {
			t.Errorf("child %q must not be a separate removal when its parent is removed", child)
		}
	}
}

// Cloud entries that match an exclusion rule must be left alone: their local
// counterpart may exist and simply have been excluded from the scan.
func TestPlanDoesNotRemoveCloudEntriesMatchingExclusions(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "keep.txt|keep")

	fs := newFakeFS().addTree("/root",
		"keep.txt|4",
		"scratch.tmp|100",
		"~$draft.docx|100",
	)

	plan := planFor(t, fs, local, PlanOptions{})

	removed := strings.Join(kindsOf(plan.Removes), ",")
	if strings.Contains(removed, ".tmp") {
		t.Errorf("an excluded .tmp must not be removed, got %v", removed)
	}
	if strings.Contains(removed, "~$draft.docx") {
		t.Errorf("an excluded ~$ file must not be removed, got %v", removed)
	}
	if !hasRel(plan.Removes, "keep.txt") && hasRel(plan.Removes, "keep.txt") {
		t.Error("keep.txt exists locally and must not be removed")
	}
	if len(plan.SkipNotices) == 0 {
		t.Error("the report must explain that excluded cloud entries were kept")
	}
}

func TestPlanIncompleteScanIsRecordedAsBlocker(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "a.txt|a", "sub/")

	fs := newFakeFS().addTree("/root", "a.txt|1", "sub/", "sub/b.txt|2")
	fs.failList["/root/sub"] = &RemoteError{Op: "列出云端目录", Path: "/root/sub", Msg: "网络错误"}

	plan := planFor(t, fs, local, PlanOptions{})

	if plan.ScanComplete {
		t.Fatal("a failed subtree must mark the plan's cloud scan incomplete")
	}
	rep := BuildPlanReport(plan, "plan")
	found := false
	for _, b := range rep.Blockers {
		if strings.Contains(b, "枚举不完整") {
			found = true
		}
	}
	if !found {
		t.Errorf("an incomplete scan must be reported as a blocker, got %v", rep.Blockers)
	}
}

func TestPlanRefusesDangerousRoots(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "a.txt|a")
	fs := newFakeFS().addTree("/root", "a.txt|1")

	for _, remote := range []string{"/", ""} {
		if _, err := BuildPlan(fs, PlanOptions{LocalRoot: local, RemoteRoot: remote}); err == nil {
			t.Errorf("remote root %q must be refused", remote)
		}
	}
	if _, err := BuildPlan(fs, PlanOptions{LocalRoot: "", RemoteRoot: "/root"}); err == nil {
		t.Error("an empty local root must be refused")
	}
	if _, err := BuildPlan(nil, PlanOptions{LocalRoot: local, RemoteRoot: "/root"}); err == nil {
		t.Error("a nil reader must be refused")
	}
}

func TestPlanZeroDifferenceOnIdenticalTrees(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "a.txt|aaaa", "b/c.txt|cccc", "empty/")

	// Hash the real content so equality is provable.
	ha, err := LocalContentHash(filepath.Join(local, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	hc, err := LocalContentHash(filepath.Join(local, "b", "c.txt"))
	if err != nil {
		t.Fatal(err)
	}

	fs := newFakeFS()
	fs.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}
	fs.metas["/root/b"] = RemoteEntry{Path: "/root/b", Name: "b", IsDir: true}
	fs.metas["/root/empty"] = RemoteEntry{Path: "/root/empty", Name: "empty", IsDir: true}
	fs.dirs["/root"] = []RemoteEntry{
		{Path: "/root/a.txt", Name: "a.txt", Size: 4, ContentMD5: ha.MD5, ContentMD5Trusted: true, BlockCount: 1},
		{Path: "/root/b", Name: "b", IsDir: true},
		{Path: "/root/empty", Name: "empty", IsDir: true},
	}
	fs.metas["/root/a.txt"] = fs.dirs["/root"][0]
	fs.dirs["/root/empty"] = nil
	fs.dirs["/root/b"] = []RemoteEntry{
		{Path: "/root/b/c.txt", Name: "c.txt", Size: 4, ContentMD5: hc.MD5, ContentMD5Trusted: true, BlockCount: 1},
	}
	fs.metas["/root/b/c.txt"] = fs.dirs["/root/b"][0]

	plan := planFor(t, fs, local, PlanOptions{})

	if !plan.ZeroDifference() {
		t.Errorf("expected zero difference, got uploads=%v replaces=%v removes=%v mkdirs=%v",
			kindsOf(plan.Uploads), kindsOf(plan.Replaces), kindsOf(plan.Removes), kindsOf(plan.Mkdirs))
	}
	if len(plan.Conflicts) != 0 {
		t.Errorf("expected no conflicts, got %v", kindsOf(plan.Conflicts))
	}
	rep := BuildPlanReport(plan, "plan")
	if !rep.Summary.ZeroDifference {
		t.Error("report must say zero difference")
	}
	if !rep.ScanComplete {
		t.Errorf("report must say the cloud scan was complete; plan errors=%v blockeders=%v",
			plan.ScanErrors, rep.Blockers)
	}
}

// A recently modified local file must still be planned: in a manually
// triggered tool the likeliest reason is that the user just edited it. The
// protection against a half-written file is the execution-time snapshot
// re-check, not a refusal to plan.
func TestCheckLocalUnchangedDetectsSnapshotDrift(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "f.txt|original")
	entry := LocalEntry{RelPath: "f.txt", AbsPath: filepath.Join(local, "f.txt")}
	info, err := os.Stat(entry.AbsPath)
	if err != nil {
		t.Fatal(err)
	}
	entry.Size = info.Size()
	entry.Mtime = info.ModTime().Unix()

	if err := CheckLocalUnchanged(entry); err != nil {
		t.Fatalf("an unchanged file must pass: %v", err)
	}
	// Grow the file: the snapshot no longer describes it.
	writeLocal(t, local, "f.txt|original plus more bytes")
	if err := CheckLocalUnchanged(entry); err == nil {
		t.Fatal("a file that changed size must be rejected")
	} else if !strings.Contains(err.Error(), "大小已改变") {
		t.Errorf("the error should name the change, got %q", err.Error())
	}
}

func TestPlanUnsafeCloudNameBecomesConflictNotDeletion(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "ok.txt|x")

	fs := newFakeFS().addTree("/root", "ok.txt|1", "bad:name.txt|2")

	plan := planFor(t, fs, local, PlanOptions{})

	if !hasRel(plan.Conflicts, "bad:name.txt") {
		t.Errorf("an unmappable cloud name must be CONFLICT, got %v", kindsOf(plan.Conflicts))
	}
	if hasRel(plan.Removes, "bad:name.txt") {
		t.Error("an unmappable cloud name must never be auto-removed")
	}
}

// The most dangerous failure mode in this whole feature: if the session expires
// while enumerating, the directories that could not be read must NOT look like
// "the user deleted them". Without a hard abort, a mirror would schedule the
// entire unread remainder of the account for removal.
func TestPlanAbortsOnAuthExpiryInsteadOfSchedulingRemovals(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "only-local.txt|x", "a/")

	fs := newFakeFS().addTree("/root",
		"a/", "a/important1.txt|1", "a/important2.txt|2",
		"b/", "b/important3.txt|3",
	)
	// Simulate the session dying part-way through: the very first subtree
	// listing reports "please log in again".
	fs.failList["/root/a"] = &RemoteError{
		Op: "列出云端目录", Path: "/root/a",
		ErrType: 2, Code: -6, Msg: "请重新登录",
	}

	_, err := BuildPlan(fs, PlanOptions{
		LocalRoot: local, RemoteRoot: "/root", EditingWindow: -1,
	})
	if err == nil {
		t.Fatal("an expired session must abort plan generation, not produce a plan")
	}
	if !IsAuthExpired(err) {
		t.Fatalf("expected an AuthExpiredError, got %T: %v", err, err)
	}
	// The message must tell the operator what to do.
	if !strings.Contains(err.Error(), "重新登录") {
		t.Errorf("error should tell the operator to log in again, got %q", err.Error())
	}
}

// A non-auth listing failure must still allow a plan (marked incomplete and
// blocked), but it must never turn unread cloud content into removals.
func TestPlanIgnoresWindowsSystemArtifactsOnBothSides(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "keep.txt|keep", "desktop.ini|settings", "sub/", "sub/Thumbs.db|thumbs")

	fs := newFakeFS().addTree("/root",
		"keep.txt|4",
		"desktop.ini|8",
		"sub/",
		"sub/Thumbs.db|6",
		"Thumbs.db|6",
	)

	plan := planFor(t, fs, local, PlanOptions{})

	for _, name := range []string{"desktop.ini", "sub/Thumbs.db", "Thumbs.db"} {
		if hasRel(plan.Uploads, name) {
			t.Errorf("%s must not be uploaded as user content", name)
		}
		if hasRel(plan.Removes, name) {
			t.Errorf("%s must not be removed as if it were redundant user content", name)
		}
	}
	// Everything below sub/ is artifacts, so sub/ itself has no local
	// counterpart that matters and must not be scheduled for removal either
	// (a cloud-only directory holding only excluded names is left alone).
	if hasRel(plan.Removes, "sub") {
		t.Error("a directory containing only excluded artefacts must not be removed")
	}
}

func TestDefaultSkipNamesCoversWindowsArtifacts(t *testing.T) {
	opt := LocalScanOptions{}
	for _, name := range []string{"desktop.ini", "Desktop.INI", "Thumbs.db", "thumbs.db", ".DS_Store"} {
		if !shouldSkipLocal(name, opt) {
			t.Errorf("%s should be excluded by default", name)
		}
	}
	for _, name := range []string{"desktop.txt", "notes.ini", "Thumbs.db.bak"} {
		if shouldSkipLocal(name, opt) {
			t.Errorf("%s is real user content and must not be excluded", name)
		}
	}
}
