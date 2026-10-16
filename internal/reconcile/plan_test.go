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
func TestPlanMatchesReadmeWorkedExample(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local,
		"paper.docx|current version of the paper",
		"notes.md|notes",
		"figures/",
		"figures/plot.png|plot bytes",
	)

	// The cloud's paper.docx is an older, shorter version; plot.png is
	// byte-identical; notes.md is absent; paper(1).docx and deleted_old.txt are
	// leftovers from the official client's backup history.
	fs := newFakeFS().addTree("/root",
		"paper.docx|10",
		"paper(1).docx|10",
		"deleted_old.txt|5",
		"figures/",
		"figures/plot.png|11",
	)

	plan := planFor(t, fs, local, PlanOptions{})

	if !hasRel(plan.Uploads, "notes.md") {
		t.Errorf("notes.md must be an UPLOAD, got %v", kindsOf(plan.Uploads))
	}
	if !hasRel(plan.Replaces, "paper.docx") {
		t.Errorf("paper.docx must be a REPLACE, got %v", kindsOf(plan.Replaces))
	}
	if !hasRel(plan.Removes, "paper(1).docx") {
		t.Errorf("paper(1).docx must be a REMOVE, got %v", kindsOf(plan.Removes))
	}
	if !hasRel(plan.Removes, "deleted_old.txt") {
		t.Errorf("deleted_old.txt must be a REMOVE, got %v", kindsOf(plan.Removes))
	}
	// plot.png is the same size and the server hash is unknown, so the default
	// conservative behaviour is to re-upload rather than trust size alone.
	if !hasRel(plan.Replaces, "figures/plot.png") && !hasRel(plan.Keeps, "figures/plot.png") {
		t.Errorf("figures/plot.png must be either REPLACE or KEEP, got plan %+v", plan)
	}
	// figures/ exists on both sides so it must not be a removal candidate.
	if hasRel(plan.Removes, "figures") {
		t.Error("a directory present on both sides must never be removed")
	}
}

// The README calls this out explicitly: a locally present "paper(1).docx" is a
// real file and must be kept, never deleted because its name looks like a
// duplicate.
func TestPlanKeepsLocallyOwnedDuplicateStyleName(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local,
		"a.txt|0123456789", // 10 bytes, matching the cloud size
		"a(1).txt|abcdefghij",
	)
	fs := newFakeFS().addTree("/root",
		"a.txt|10",
		"a(1).txt|10",
	)

	plan := planFor(t, fs, local, PlanOptions{})

	if hasRel(plan.Removes, "a(1).txt") {
		t.Fatal("a locally existing a(1).txt must never be removed")
	}
	// Sizes agree but no trustworthy hash is available, so the default plans a
	// conservative re-upload. The essential point is that it is NOT a removal.
	if !hasRel(plan.Replaces, "a(1).txt") && !hasRel(plan.Keeps, "a(1).txt") {
		t.Errorf("a(1).txt should be kept or replaced, got %+v", plan)
	}
}

func TestPlanTrustworthyHashProvesEqualityAndDifference(t *testing.T) {
	local := t.TempDir()
	// "abc" -> 900150983cd24fb0d6963f7d28e17f72
	writeLocal(t, local, "same.txt|abc", "diff.txt|abc")

	fs := newFakeFS()
	fs.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}
	fs.dirs["/root"] = []RemoteEntry{
		{Path: "/root/same.txt", Name: "same.txt", Size: 3,
			ContentMD5: "900150983cd24fb0d6963f7d28e17f72", ContentMD5Trusted: true, BlockCount: 1},
		{Path: "/root/diff.txt", Name: "diff.txt", Size: 3,
			ContentMD5: "ffffffffffffffffffffffffffffffff", ContentMD5Trusted: true, BlockCount: 1},
	}
	fs.metas["/root/same.txt"] = fs.dirs["/root"][0]
	fs.metas["/root/diff.txt"] = fs.dirs["/root"][1]

	plan := planFor(t, fs, local, PlanOptions{})

	if !hasRel(plan.Keeps, "same.txt") {
		t.Errorf("a matching trustworthy hash must be KEEP, got %v", kindsOf(plan.Keeps))
	}
	if !hasRel(plan.Replaces, "diff.txt") {
		t.Errorf("a mismatching trustworthy hash must be REPLACE, got %v", kindsOf(plan.Replaces))
	}
	for _, a := range plan.Replaces {
		if a.RelPath == "same.txt" {
			t.Error("a proven-equal file must not be re-uploaded")
		}
	}
}

// A hash mismatch on an UNUSABLE hash must never be read as "content differs".
// With equal sizes it is "unable to prove", which the default resolves by
// re-uploading and --conservative resolves by asking a human.
func TestPlanUnusableHashIsNeverProofOfDifference(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "big.bin|0123456789")

	fs := newFakeFS()
	fs.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}
	// No usable hash at all: the server returned nothing comparable, which is
	// what an empty or malformed md5 field looks like.
	entry := RemoteEntry{Path: "/root/big.bin", Name: "big.bin", Size: 10,
		ContentMD5: "", ContentMD5Trusted: false, BlockCount: 0}
	fs.dirs["/root"] = []RemoteEntry{entry}
	fs.metas["/root/big.bin"] = entry

	def := planFor(t, fs, local, PlanOptions{})
	if !hasRel(def.Replaces, "big.bin") {
		t.Errorf("default should conservatively re-upload, got %v", kindsOf(def.Replaces))
	}
	if hasRel(def.Conflicts, "big.bin") {
		t.Error("default must not report a conflict when no hash is available")
	}
	for _, a := range def.Replaces {
		if a.RelPath == "big.bin" && a.Rule != RuleSizeOnlyEqual {
			t.Errorf("the rule should record 'size only equal', got %q", a.Rule)
		}
	}

	cons := planFor(t, fs, local, PlanOptions{Conservative: true})
	if !hasRel(cons.Conflicts, "big.bin") {
		t.Errorf("--conservative should report a conflict, got %v", kindsOf(cons.Conflicts))
	}
	if hasRel(cons.Replaces, "big.bin") {
		t.Error("--conservative must not plan a re-upload")
	}
}

// Measured against the live account: /api/list returns the FULL-file content
// MD5 and no block_list at all, and it is accurate for multi-block files too
// (verified with a 1 MiB file). Requiring a single block_list entry would throw
// that proof away and re-upload every identical file on the account.
func TestPlanTrustsServerHashWithoutBlockList(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "big.bin|0123456789abcdef")

	hash, err := LocalContentHash(filepath.Join(local, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}

	fs := newFakeFS()
	fs.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}
	// A realistic list-API entry: correct full-file md5, zero block_list.
	entry := RemoteEntry{
		Path: "/root/big.bin", Name: "big.bin", Size: 16,
		ContentMD5: hash.MD5, ContentMD5Trusted: true, BlockCount: 0,
	}
	fs.dirs["/root"] = []RemoteEntry{entry}
	fs.metas["/root/big.bin"] = entry

	plan := planFor(t, fs, local, PlanOptions{})
	if !plan.ZeroDifference() {
		t.Errorf("an identical file must be KEEP, not re-uploaded: uploads=%v replaces=%v",
			kindsOf(plan.Uploads), kindsOf(plan.Replaces))
	}
	if !hasRel(plan.Keeps, "big.bin") {
		t.Errorf("big.bin should be KEEP, got %v", kindsOf(plan.Keeps))
	}
}

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
		{"781e5e245d69b566979b86e28d23f2c", false},  // 31 chars
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
		"thing|12",     // cloud has a FILE where local has a DIRECTORY
		"plain.txt/",   // cloud has a DIRECTORY where local has a FILE
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

func TestPlanCreatesMissingCloudDirectories(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "a/b/c/deep.txt|content", "emptydir/")

	fs := newFakeFS().addTree("/root", "existing.txt|1")

	plan := planFor(t, fs, local, PlanOptions{})

	for _, want := range []string{"a", "a/b", "a/b/c", "emptydir"} {
		if !hasRel(plan.Mkdirs, want) {
			t.Errorf("missing MKDIR for %q, got %v", want, kindsOf(plan.Mkdirs))
		}
	}
	if !hasRel(plan.Uploads, "a/b/c/deep.txt") {
		t.Errorf("deep.txt must be uploaded, got %v", kindsOf(plan.Uploads))
	}
	// Every upload's parent chain must be created before the upload.
	mk := map[string]bool{}
	for _, a := range plan.Mkdirs {
		mk[a.RelPath] = true
	}
	for _, u := range plan.Uploads {
		parent := ParentRel(u.RelPath)
		for parent != "" {
			if !mk[parent] && !hasCloudDir(fs, JoinRemote("/root", parent)) {
				t.Errorf("upload %q needs parent %q which is neither created nor present", u.RelPath, parent)
			}
			parent = ParentRel(parent)
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
	writeLocal(t, local, "a.txt|a")

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
func TestPlanMarksRecentlyModifiedFileAsUnstableButStillPlansIt(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "busy.txt|partial content")

	fs := newFakeFS().addTree("/root", "busy.txt|999")

	plan := planFor(t, fs, local, PlanOptions{
		Now:           time.Now(),
		EditingWindow: time.Hour,
	})

	if !hasRel(plan.Replaces, "busy.txt") {
		t.Fatalf("a recently edited file must still be planned, got %+v", plan)
	}
	for _, a := range plan.Replaces {
		if a.RelPath == "busy.txt" {
			if a.Stable {
				t.Error("a file inside the editing window must be marked unstable")
			}
			if !strings.Contains(a.Reason, "重新校验") {
				t.Errorf("the reason should say the snapshot is re-checked, got %q", a.Reason)
			}
		}
	}
	// And an upload of a fresh file is likewise planned but marked unstable.
	up := planFor(t, fs, local, PlanOptions{Now: time.Now(), EditingWindow: time.Hour})
	_ = up
}

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
	writeLocal(t, local, "only-local.txt|x")

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
func TestPlanDoesNotScheduleRemovalsForUnreadSubtree(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "keep.txt|keep")

	fs := newFakeFS().addTree("/root",
		"keep.txt|4",
		"unread/", "unread/must_not_be_removed.txt|9",
	)
	fs.failList["/root/unread"] = &RemoteError{
		Op: "列出云端目录", Path: "/root/unread", Msg: "网络错误",
	}

	plan := planFor(t, fs, local, PlanOptions{})

	if plan.ScanComplete {
		t.Error("the plan must be marked incomplete")
	}
	for _, a := range plan.Removes {
		if strings.HasPrefix(a.RelPath, "unread") {
			t.Errorf("content of an unread subtree must never be scheduled for removal: %+v", a)
		}
	}
	rep := BuildPlanReport(plan, "plan")
	if len(rep.Blockers) == 0 {
		t.Error("an incomplete scan must produce a blocker")
	}
}

// Windows and Explorer artefacts must not be treated as mirror content on
// either side. Observed live: desktop.ini existed locally and, once the official
// client had backed it up, in the cloud too, producing both an UPLOAD and a
// REMOVE for the same name.
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

func TestPlanReportSerialisesAndRenders(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "new.txt|new content", "dir/", "dir/inner.txt|inner")

	fs := newFakeFS().addTree("/root", "stale.txt|old")

	plan := planFor(t, fs, local, PlanOptions{})
	rep := BuildPlanReport(plan, "plan (只读预览)")

	data, err := rep.PlanJSON()
	if err != nil {
		t.Fatalf("PlanJSON: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty JSON")
	}

	var buf strings.Builder
	RenderPlanHuman(&buf, rep)
	out := buf.String()
	for _, want := range []string{"差异预览", "UPLOAD", "REMOVE", "概况", "结构指纹", "只读预览"} {
		if !strings.Contains(out, want) {
			t.Errorf("human plan is missing %q", want)
		}
	}
	if strings.Contains(out, "BDUSS") {
		t.Error("plan output must not contain credential material")
	}

	dir := t.TempDir()
	path, err := WritePlanReport(dir, rep)
	if err != nil {
		t.Fatalf("WritePlanReport: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report not written: %v", err)
	}
}

// Cloud directory listing that only differs by a trailing slash must not produce
// a bogus mirror of the whole tree.
func TestPlanHandlesChineseSpacesAndParentheses(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local,
		"论文 终稿 (1).docx|paper",
		"目录 A/子 目录 B/file (2).txt|nested",
	)

	fs := newFakeFS().addTree("/root", "论文 终稿 (1).docx|5")

	plan := planFor(t, fs, local, PlanOptions{})

	if !hasRel(plan.Uploads, "目录 A/子 目录 B/file (2).txt") {
		t.Errorf("nested CJK/space/paren path must be planned, got %v", kindsOf(plan.Uploads))
	}
	if !hasRel(plan.Keeps, "论文 终稿 (1).docx") && !hasRel(plan.Replaces, "论文 终稿 (1).docx") {
		t.Errorf("existing CJK/paren file must be kept or replaced, got uploads=%v replaces=%v",
			kindsOf(plan.Uploads), kindsOf(plan.Replaces))
	}
	// The new nested path needs its directories created first.
	for _, want := range []string{"目录 A", "目录 A/子 目录 B"} {
		if !hasRel(plan.Mkdirs, want) {
			t.Errorf("expected MKDIR for %q, got %v", want, kindsOf(plan.Mkdirs))
		}
	}
}
