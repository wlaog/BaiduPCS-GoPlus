package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWriter is an in-memory Writer that records every mutation, so tests can
// assert both what happened and what deliberately did not.
type fakeWriter struct {
	dirs map[string]bool
	// files maps remote path -> size
	files map[string]int64

	mkdirCalls  []string
	uploadCalls []string
	moveCalls   [][2]string
	removeCalls []string

	// failMkdir / failUpload / failMove / failRemove abort that operation for
	// the listed paths.
	failMkdir  map[string]bool
	failUpload map[string]bool
	failMove   map[string]bool
	failRemove map[string]bool

	// dropUpload makes an upload silently not take effect, to exercise the
	// post-write verification pass.
	dropUpload map[string]bool
}

func newFakeWriter() *fakeWriter {
	return &fakeWriter{
		dirs:       map[string]bool{"/": true},
		files:      map[string]int64{},
		failMkdir:  map[string]bool{},
		failUpload: map[string]bool{},
		failMove:   map[string]bool{},
		failRemove: map[string]bool{},
		dropUpload: map[string]bool{},
	}
}

func (f *fakeWriter) Mkdir(p string) error {
	if f.failMkdir[p] {
		return errors.New("mkdir failed")
	}
	f.mkdirCalls = append(f.mkdirCalls, p)
	f.dirs[p] = true
	return nil
}

func (f *fakeWriter) ListDir(p string) ([]RemoteEntry, error) {
	var out []RemoteEntry
	prefix := p
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	for d := range f.dirs {
		if strings.HasPrefix(d, prefix) && d != p && !strings.Contains(strings.TrimPrefix(d, prefix), "/") {
			out = append(out, RemoteEntry{Path: d, Name: baseName(d), IsDir: true})
		}
	}
	for fp, size := range f.files {
		if strings.HasPrefix(fp, prefix) && !strings.Contains(strings.TrimPrefix(fp, prefix), "/") {
			out = append(out, RemoteEntry{Path: fp, Name: baseName(fp), Size: size})
		}
	}
	sortRemoteEntries(out)
	return out, nil
}

func (f *fakeWriter) Meta(p string) (RemoteEntry, error) {
	if f.dirs[p] {
		return RemoteEntry{Path: p, Name: baseName(p), IsDir: true}, nil
	}
	if size, ok := f.files[p]; ok {
		return RemoteEntry{Path: p, Name: baseName(p), Size: size}, nil
	}
	return RemoteEntry{}, &NotFoundError{Path: p}
}

func (f *fakeWriter) Upload(localPath, remotePath string, size int64) error {
	if f.failUpload[remotePath] {
		return errors.New("upload failed")
	}
	f.uploadCalls = append(f.uploadCalls, remotePath)
	if !f.dropUpload[remotePath] {
		f.files[remotePath] = size
	}
	return nil
}

func (f *fakeWriter) Move(from, to string) error {
	if f.failMove[from] {
		return errors.New("move failed")
	}
	f.moveCalls = append(f.moveCalls, [2]string{from, to})
	if size, ok := f.files[from]; ok {
		delete(f.files, from)
		f.files[to] = size
		return nil
	}
	if f.dirs[from] {
		delete(f.dirs, from)
		f.dirs[to] = true
		return nil
	}
	return &NotFoundError{Path: from}
}

func (f *fakeWriter) Remove(paths ...string) error {
	for _, p := range paths {
		if f.failRemove[p] {
			return errors.New("remove failed")
		}
		f.removeCalls = append(f.removeCalls, p)
		delete(f.files, p)
		delete(f.dirs, p)
	}
	return nil
}

// buildApplyFixture produces a plan plus a writer already holding the cloud
// side, mirroring what a real run would see.
func buildApplyFixture(t *testing.T, opt PlanOptions) (*Plan, *fakeWriter) {
	t.Helper()
	local := t.TempDir()
	writeLocal(t, local,
		"new.txt|brand new content",
		"changed.txt|local version",
		"dir/",
		"dir/keep.txt|nested",
	)
	fs := newFakeFS().addTree("/root",
		"changed.txt|1", // size differs -> REPLACE
		"stale.txt|5",   // only on cloud -> REMOVE
	)

	opt.LocalRoot = local
	opt.RemoteRoot = "/root"
	if opt.EditingWindow == 0 {
		opt.EditingWindow = -1
	}
	plan, err := BuildPlan(fs, opt)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	w := newFakeWriter()
	w.files["/root/changed.txt"] = 1
	w.files["/root/stale.txt"] = 5
	return plan, w
}

func applyWith(t *testing.T, plan *Plan, w *fakeWriter, opt ApplyOptions) *ApplyReport {
	t.Helper()
	if opt.PlanOptions.LocalRoot == "" {
		opt.PlanOptions = PlanOptions{LocalRoot: plan.LocalRoot, RemoteRoot: plan.RemoteRoot}
	}
	e := &Executor{Writer: w, Now: func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }}
	rep, err := e.Apply(plan, opt)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return rep
}

func TestApplyUploadsBeforeRemoving(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	rep := applyWith(t, plan, w, ApplyOptions{ConfirmRemoval: true})

	if rep.UploadFailures != 0 {
		t.Fatalf("no uploads should fail: %+v", rep.Uploads)
	}
	if !strings.Contains(strings.Join(w.uploadCalls, ","), "/root/new.txt") {
		t.Errorf("new.txt must be uploaded, got %v", w.uploadCalls)
	}
	if len(w.moveCalls) == 0 {
		t.Error("stale.txt should have been moved to quarantine")
	}
	for _, mv := range w.moveCalls {
		if !strings.Contains(mv[1], QuarantineDirName) {
			t.Errorf("removals must go to quarantine, got %v", mv)
		}
	}
	if len(w.removeCalls) != 0 {
		t.Errorf("quarantine mode must not permanently delete anything, got %v", w.removeCalls)
	}
	if !rep.Succeeded() {
		t.Errorf("expected success, removals skipped=%v aborted=%v", rep.RemovalSkipped, rep.AbortReason)
	}
}

// The single most important sequencing rule: a cloud copy is only redundant once
// the local one is confirmed uploaded.
func TestApplySkipsRemovalsWhenAnyUploadFails(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	w.failUpload["/root/new.txt"] = true

	rep := applyWith(t, plan, w, ApplyOptions{ConfirmRemoval: true})

	if rep.UploadFailures == 0 {
		t.Fatal("expected a recorded upload failure")
	}
	if !rep.RemovalSkipped {
		t.Error("removals must be skipped when an upload failed")
	}
	if len(w.moveCalls) != 0 {
		t.Errorf("nothing may be removed after a failed upload, got %v", w.moveCalls)
	}
	if len(w.removeCalls) != 0 {
		t.Errorf("nothing may be deleted after a failed upload, got %v", w.removeCalls)
	}
}

// An upload that reports success but does not take effect must be caught before
// removal, otherwise the cloud copy would be moved away with no replacement.
func TestApplyVerifiesUploadsBeforeRemoving(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	w.dropUpload["/root/new.txt"] = true // appears to succeed, silently no-ops

	rep := applyWith(t, plan, w, ApplyOptions{ConfirmRemoval: true})

	if len(rep.Verifies) == 0 {
		t.Fatal("expected a verification pass")
	}
	if !rep.RemovalSkipped {
		t.Error("a verification failure must skip removals")
	}
	if len(w.moveCalls) != 0 {
		t.Errorf("nothing may be removed when verification failed, got %v", w.moveCalls)
	}
}

func TestApplyRequiresExplicitRemovalConfirmation(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})

	rep := applyWith(t, plan, w, ApplyOptions{ConfirmRemoval: false})

	if !rep.RemovalSkipped {
		t.Error("removal must not run without confirmation")
	}
	if len(w.moveCalls) != 0 {
		t.Errorf("no removal may happen without confirmation, got %v", w.moveCalls)
	}
	// Uploads should still have happened: the operator asked for the safe half.
	if len(w.uploadCalls) == 0 {
		t.Error("uploads should proceed even when removal is not confirmed")
	}
}

func TestApplyRefusesRecycleWithoutExtraAcknowledgement(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})

	rep := applyWith(t, plan, w, ApplyOptions{
		ConfirmRemoval: true, RemovalMode: RemovalRecycle, AcknowledgeRecycle: false,
	})
	if !rep.RemovalSkipped {
		t.Error("the recycle mode needs its own acknowledgement")
	}
	if len(w.removeCalls) != 0 || len(w.moveCalls) != 0 {
		t.Error("nothing should be removed without that acknowledgement")
	}

	plan2, w2 := buildApplyFixture(t, PlanOptions{})
	rep2 := applyWith(t, plan2, w2, ApplyOptions{
		ConfirmRemoval: true, RemovalMode: RemovalRecycle, AcknowledgeRecycle: true,
	})
	if rep2.RemovalSkipped {
		t.Errorf("with acknowledgement the recycle mode should run: %s", rep2.RemovalReason)
	}
	if len(w2.removeCalls) == 0 {
		t.Error("recycle mode should call Remove")
	}
	if len(w2.moveCalls) != 0 {
		t.Errorf("recycle mode should not move to quarantine, got %v", w2.moveCalls)
	}
}

// A failed move must stop the removal phase, never degrade into a delete.
func TestApplyNeverDegradesFailedMoveIntoDelete(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	w.failMove["/root/stale.txt"] = true

	rep := applyWith(t, plan, w, ApplyOptions{ConfirmRemoval: true})

	if len(w.removeCalls) != 0 {
		t.Fatalf("a failed move must never become a permanent delete, got %v", w.removeCalls)
	}
	if len(rep.Warnings) == 0 {
		t.Error("the failed move must be reported")
	}
}

func TestApplyBlastRadiusThresholds(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})

	// 1 file of 2 cloud files = 50%, above a 10% threshold.
	rep := applyWith(t, plan, w, ApplyOptions{
		ConfirmRemoval: true, MaxRemovePercent: 10,
	})
	if !rep.RemovalSkipped {
		t.Error("a removal above the percentage threshold must be refused")
	}
	if len(w.moveCalls) != 0 {
		t.Errorf("nothing may be removed when the threshold trips, got %v", w.moveCalls)
	}
	if !strings.Contains(rep.RemovalReason, "阈值") {
		t.Errorf("the reason should mention the threshold, got %q", rep.RemovalReason)
	}

	plan2, w2 := buildApplyFixture(t, PlanOptions{})
	rep2 := applyWith(t, plan2, w2, ApplyOptions{
		ConfirmRemoval: true, MaxRemovePercent: 10, AcknowledgeBlast: true,
	})
	if rep2.RemovalSkipped {
		t.Errorf("acknowledging the blast radius should allow it: %s", rep2.RemovalReason)
	}
}

func TestApplyRefusesQuarantineInsideMirror(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	e := &Executor{Writer: w}
	_, err := e.Apply(plan, ApplyOptions{
		ConfirmRemoval: true,
		QuarantineRoot: "/root/_quarantine", // inside the mirror root
	})
	if err == nil {
		t.Fatal("a quarantine inside the mirror root must be refused")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("expected ErrUsage, got %v", err)
	}
	if len(w.moveCalls) != 0 {
		t.Error("nothing may happen when the quarantine location is invalid")
	}
}

func TestApplyRefusesIncompleteScan(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	plan.ScanComplete = false

	e := &Executor{Writer: w}
	rep, err := e.Apply(plan, ApplyOptions{ConfirmRemoval: true})
	if err == nil {
		t.Fatal("an incomplete scan must refuse execution")
	}
	if !rep.Aborted {
		t.Error("the report must record the abort")
	}
	if len(w.uploadCalls) != 0 || len(w.moveCalls) != 0 {
		t.Error("nothing may be written when the scan is incomplete")
	}
}

// A file that changed between planning and execution must not be uploaded.
func TestApplySkipsLocalFileThatChangedSincePlanning(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	// Grow a planned upload target after the plan was built.
	target := filepath.Join(plan.LocalRoot, "new.txt")
	if err := os.WriteFile(target, []byte("changed after planning, longer content"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := applyWith(t, plan, w, ApplyOptions{ConfirmRemoval: true})

	var skipped bool
	for _, res := range rep.Uploads {
		if res.Action.RelPath == "new.txt" && res.Skipped {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("a file that changed after planning must be skipped, got %+v", rep.Uploads)
	}
	for _, p := range w.uploadCalls {
		if p == "/root/new.txt" {
			t.Error("a changed file must not be uploaded")
		}
	}
}

func TestApplyCreatesParentDirectoriesBeforeUpload(t *testing.T) {
	local := t.TempDir()
	writeLocal(t, local, "a/b/c/deep.txt|deep")
	fs := newFakeFS().addTree("/root", "existing.txt|1")

	plan, err := BuildPlan(fs, PlanOptions{
		LocalRoot: local, RemoteRoot: "/root", EditingWindow: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := newFakeWriter()
	rep := applyWith(t, plan, w, ApplyOptions{})

	if rep.UploadFailures != 0 {
		t.Fatalf("uploads should succeed: %+v", rep.Uploads)
	}
	for _, want := range []string{"/root/a", "/root/a/b", "/root/a/b/c"} {
		found := false
		for _, got := range w.mkdirCalls {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected Mkdir %q, got %v", want, w.mkdirCalls)
		}
	}
}

func TestApplyReportRendersAndSerialises(t *testing.T) {
	plan, w := buildApplyFixture(t, PlanOptions{})
	rep := applyWith(t, plan, w, ApplyOptions{ConfirmRemoval: true})

	var buf strings.Builder
	RenderApplyHuman(&buf, rep)
	out := buf.String()
	for _, want := range []string{"执行结果", "上传/覆盖", "清理", "隔离区"} {
		if !strings.Contains(out, want) {
			t.Errorf("apply report is missing %q", want)
		}
	}
	if strings.Contains(out, "BDUSS") {
		t.Error("apply report must not contain credentials")
	}

	dir := t.TempDir()
	path, err := WriteApplyReport(dir, rep)
	if err != nil {
		t.Fatalf("WriteApplyReport: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report not written: %v", err)
	}
}

func TestParentRemote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/a/b/c", "/a/b"},
		{"/a", "/"},
		{"/", "/"},
		{"/a/b/", "/a"},
	}
	for _, c := range cases {
		if got := ParentRemote(c.in); got != c.want {
			t.Errorf("ParentRemote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
