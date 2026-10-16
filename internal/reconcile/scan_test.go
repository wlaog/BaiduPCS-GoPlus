package reconcile

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestJoinRemoteUsesForwardSlashesEverywhere(t *testing.T) {
	cases := []struct {
		root, rel, want string
	}{
		{"/", "", "/"},
		{"/", "a", "/a"},
		{"/root", "", "/root"},
		{"/root/", "a", "/root/a"},
		{"/root", "a\\b\\c", "/root/a/b/c"},
		{"root//sub///", "x", "/root/sub/x"},
		{"/我的资源/来自：本地电脑", "论文/paper (1).docx", "/我的资源/来自：本地电脑/论文/paper (1).docx"},
		{"/root", "./a/./b", "/root/./a/./b"},
	}
	for _, c := range cases {
		if got := JoinRemote(c.root, c.rel); got != c.want {
			t.Errorf("JoinRemote(%q, %q) = %q, want %q", c.root, c.rel, got, c.want)
		}
	}
}

func TestNormalizeRemoteRoot(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"//", "/"},
		{"foo", "/foo"},
		{"/foo/", "/foo"},
		{"/foo//bar/", "/foo/bar"},
		{`C:\x`, "/C:/x"},
	}
	for _, c := range cases {
		if got := NormalizeRemoteRoot(c.in); got != c.want {
			t.Errorf("NormalizeRemoteRoot(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsSubPathGuardsTheRoot(t *testing.T) {
	cases := []struct {
		root, child string
		want        bool
	}{
		{"/root", "/root", true},
		{"/root", "/root/sub", true},
		{"/root", "/root/sub/deep", true},
		{"/root", "/rooted", false},   // prefix but not a child
		{"/root", "/other", false},    // sibling
		{"/root", "/", false},         // the volume root
		{"/root/sub", "/root", false}, // parent
		{"/", "/anything", true},      // volume root contains everything
	}
	for _, c := range cases {
		if got := IsSubPath(c.root, c.child); got != c.want {
			t.Errorf("IsSubPath(%q, %q) = %v, want %v", c.root, c.child, got, c.want)
		}
	}
}

func TestAssertInsideRootRejectsEscape(t *testing.T) {
	if err := assertInsideRoot("/root", "/root/a/b"); err != nil {
		t.Fatalf("expected inside-root path to pass, got %v", err)
	}
	for _, target := range []string{"/other/a", "/rooted", "/", ""} {
		err := assertInsideRoot("/root", target)
		if err == nil {
			t.Fatalf("expected %q to be rejected", target)
		}
		if !errors.Is(err, ErrUsage) {
			t.Fatalf("expected ErrUsage for %q, got %v", target, err)
		}
	}
}

func TestUnsafeNameReason(t *testing.T) {
	cases := []struct {
		name   string
		unsafe bool
	}{
		{"paper.docx", false},
		{"论文 2024 (final).docx", false},
		{"figures", false},
		{"a<b.txt", true},
		{"a>b.txt", true},
		{"a:b.txt", true},
		{"a|b.txt", true},
		{"a?b.txt", true},
		{"a*b.txt", true},
		{"a\"b.txt", true},
		{"a/b.txt", true},
		{`a\b.txt`, true},
		{"CON", true},
		{"con.txt", true},
		{"COM1.txt", true},
		{"COM0.txt", false},
		{"trailing.", true},
		{"trailing ", true},
		{"", true},
		{".", true},
		{"..", true},
		{"ctrl\x01char", true},
	}
	for _, c := range cases {
		reason := UnsafeNameReason(c.name)
		if got := reason != ""; got != c.unsafe {
			t.Errorf("UnsafeNameReason(%q) = %q; unsafe=%v, want %v", c.name, reason, got, c.unsafe)
		}
	}
}

func TestScanCloudRecordsTreeAndStats(t *testing.T) {
	fs := newFakeFS().addTree("/root",
		"paper.docx|1000|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"notes.md|10|bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"figures/",
		"figures/plot.png|2000|cccccccccccccccccccccccccccccccc",
		"empty/",
	)

	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if !scan.ScanComplete() {
		t.Fatalf("expected complete scan, got errors %+v", scan.Stats.Errors)
	}
	if scan.Stats.FileCount != 3 {
		t.Errorf("FileCount = %d, want 3", scan.Stats.FileCount)
	}
	if scan.Stats.DirCount != 2 {
		t.Errorf("DirCount = %d, want 2 (figures, empty)", scan.Stats.DirCount)
	}
	if scan.Stats.TotalBytes != 3010 {
		t.Errorf("TotalBytes = %d, want 3010", scan.Stats.TotalBytes)
	}
	if _, ok := scan.Entries["/root/figures/plot.png"]; !ok {
		t.Errorf("missing nested entry /root/figures/plot.png; got %v", scan.Order)
	}
	if !scan.Entries["/root/paper.docx"].ContentMD5Trusted {
		t.Errorf("a syntactically valid server md5 must be flagged usable")
	}
	if !scan.Dirs["/root/empty"] {
		t.Errorf("empty directory should still be recorded so it can be mirrored")
	}
}

func TestScanCloudEmptyRootIsNotAnError(t *testing.T) {
	fs := newFakeFS().addTree("/root", "empty/")
	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if scan.Stats.FileCount != 0 || scan.Stats.DirCount != 1 {
		t.Fatalf("unexpected stats %+v", scan.Stats)
	}
	if !scan.ScanComplete() {
		t.Fatalf("expected complete scan")
	}
}

func TestScanCloudRefusesMissingRoot(t *testing.T) {
	fs := newFakeFS().addTree("/other", "a.txt|1")
	_, err := ScanCloud(fs, "/root", ScanLimits{})
	if err == nil {
		t.Fatal("expected an error for a missing remote root")
	}
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("expected ErrUsage, got %v", err)
	}
	if !IsNotFound(fs.metasErr("/root")) {
		t.Fatalf("fake should report NotFound for /root")
	}
}

func TestScanCloudRefusesFileAsRoot(t *testing.T) {
	fs := newFakeFS()
	fs.metas["/root"] = RemoteEntry{Path: "/root", Name: "root", IsDir: false, Size: 5}
	fs.dirs["/root"] = nil
	_, err := ScanCloud(fs, "/root", ScanLimits{})
	if err == nil || !errors.Is(err, ErrUsage) {
		t.Fatalf("expected ErrUsage for a file root, got %v", err)
	}
}

// This is the single most important safety property of the scanner: a subtree
// that could not be listed must never look like an empty subtree, because
// "empty" would make every cloud file under it a removal candidate.
func TestScanCloudRecordsListingFailureInsteadOfPretendingEmpty(t *testing.T) {
	fs := newFakeFS().addTree("/root", "keep.txt:1", "sub/", "sub/lost.txt|2")
	fs.failList["/root/sub"] = &RemoteError{Op: "列出云端目录", Path: "/root/sub", Msg: "网络错误"}

	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud should not fail the whole scan: %v", err)
	}
	if scan.ScanComplete() {
		t.Fatal("scan must be marked incomplete when a subtree failed")
	}
	if len(scan.Stats.Errors) != 1 || scan.Stats.Errors[0].Path != "/root/sub" {
		t.Fatalf("expected the failed subtree recorded, got %+v", scan.Stats.Errors)
	}
	if _, ok := scan.Entries["/root/sub"]; !ok {
		t.Fatal("the failed directory itself must still be present so it is not treated as absent")
	}
	if _, ok := scan.Entries["/root/sub/lost.txt"]; ok {
		t.Fatal("unreachable file must not be silently invented")
	}
}

func TestScanCloudDetectsPageSizeBoundary(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["/root"] = makeSiblings("/root", PanicPageSize)
	fs.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}

	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if len(scan.Stats.LargeDirs) != 1 {
		t.Fatalf("expected the boundary directory to be flagged, got %+v", scan.Stats.LargeDirs)
	}
	if scan.Stats.LargeDirs["/root"] != PanicPageSize {
		t.Fatalf("unexpected count %+v", scan.Stats.LargeDirs)
	}
}

func TestScanCloudFlagsPaginationTruncationAtPageBoundary(t *testing.T) {
	// A server that silently stops at the page boundary must not be mistaken
	// for a directory of exactly 1000 children.
	fs := newFakeFS()
	fs.dirs["/root"] = makeSiblings("/root", PanicPageSize)
	fs.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}

	scan, err := ScanCloud(fs, "/root", ScanLimits{MaxEntries: 100})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if !scan.Stats.Truncated {
		t.Fatal("expected truncation to be reported when the entry cap is hit")
	}
	if scan.ScanComplete() {
		t.Fatal("a truncated scan must not report itself complete")
	}
}

// An intentional, caller-requested depth bound must be distinguishable from the
// safety backstop firing. Reporting the former as a failure would train the
// operator to ignore real completeness failures.
//
// Depth semantics: the root is depth 0 and its children are depth 1. Enumeration
// stops when a directory at depth >= MaxDepth would have to be entered, so
// MaxDepth = 1 records the root and its children but descends no further.
func TestScanCloudDistinguishesRequestedDepthFromSafetyBackstop(t *testing.T) {
	// Build an explicit chain /root/a/b/c/d/e so the depth limit is actually
	// reachable. DirEntry paths must be real, so this is set up by hand rather
	// than through the textual addTree helper.
	build := func() *fakeFS {
		f := newFakeFS()
		f.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}
		f.dirs["/root"] = nil
		parent := "/root"
		for _, name := range []string{"a", "b", "c", "d", "e"} {
			child := parent + "/" + name
			f.dirs[parent] = append(f.dirs[parent], RemoteEntry{Path: child, Name: name, IsDir: true})
			f.dirs[child] = nil
			f.metas[child] = RemoteEntry{Path: child, Name: name, IsDir: true}
			parent = child
		}
		// A file at the very bottom: it must not be reachable within a bound
		// that stops above it.
		f.dirs[parent] = append(f.dirs[parent], RemoteEntry{
			Path: parent + "/deep.txt", Name: "deep.txt", Size: 1,
		})
		f.metas[parent+"/deep.txt"] = RemoteEntry{Path: parent + "/deep.txt", Name: "deep.txt", Size: 1}
		return f
	}

	// Caller asked for depth 1: a bounded but valid report.
	requested, err := ScanCloud(build(), "/root", ScanLimits{MaxDepth: 1, ExpectedMaxDepth: 1})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if requested.ShortOfRequestedDepth() {
		t.Error("a requested depth bound must not be reported as an incomplete scan")
	}
	if !requested.ScanComplete() {
		t.Errorf("requested depth bound must still count as complete, errors=%+v", requested.Stats.Errors)
	}
	if len(requested.Stats.DepthBounded) == 0 {
		t.Error("the depth-bounded directories must be recorded for the report")
	}
	if !requested.Dirs["/root/a"] {
		t.Error("the entry one level below the root must still be captured")
	}
	if requested.Dirs["/root/a/b"] {
		t.Error("nothing below the requested depth may be captured")
	}

	// The safety backstop is tighter than the requested depth, so it fires
	// before the caller's requested depth is reached: that is a real gap.
	backstop, err := ScanCloud(build(), "/root", ScanLimits{MaxDepth: 2, ExpectedMaxDepth: 10})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if !backstop.ShortOfRequestedDepth() {
		t.Error("the safety backstop firing must be reported as an incomplete scan")
	}
	if backstop.ScanComplete() {
		t.Error("the safety backstop firing must not count as complete")
	}
	if len(backstop.Stats.DepthBounded) != 0 {
		t.Error("a backstop stop is not a requested bound")
	}
	if backstop.Dirs["/root/a/b/c"] {
		t.Error("the backstop must not enumerate deeper than the cap")
	}

	// A deep-enough cap reaches the bottom of the tree and is fully complete.
	full, err := ScanCloud(build(), "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if !full.ScanComplete() || full.Stats.Truncated {
		t.Errorf("an unbounded scan of this tree must be complete: %+v", full.Stats)
	}
	if _, ok := full.Entries["/root/a/b/c/d/e/deep.txt"]; !ok {
		t.Error("the deepest file must be reachable without a depth bound")
	}
}

func TestScanCloudRefusesDepthBeyondLimit(t *testing.T) {
	fs := newFakeFS().addTree("/root", "a/", "a/b/", "a/b/c/", "a/b/c/deep.txt|1")
	scan, err := ScanCloud(fs, "/root", ScanLimits{MaxDepth: 2})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if scan.ScanComplete() {
		t.Fatal("depth truncation must mark the scan incomplete")
	}
	if !scan.Stats.Truncated {
		t.Fatal("expected Truncated to be set")
	}
}

func TestScanCloudSkipsPlaceholderFiles(t *testing.T) {
	fs := newFakeFS().addTree("/root", ".bzEmpty:0", "real.txt|5")
	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if _, ok := scan.Entries["/root/.bzEmpty"]; ok {
		t.Fatal("placeholder file should not be treated as user data")
	}
	if _, ok := scan.Entries["/root/real.txt"]; !ok {
		t.Fatal("real file should be present")
	}
}

func TestScanCloudRecordsEntriesWithoutPathAsErrors(t *testing.T) {
	fs := newFakeFS()
	fs.metas["/root"] = RemoteEntry{Path: "/root", IsDir: true}
	fs.dirs["/root"] = []RemoteEntry{{Name: "ghost.txt", Size: 1}}
	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if len(scan.Stats.Errors) != 1 {
		t.Fatalf("expected a recorded error for a path-less entry, got %+v", scan.Stats.Errors)
	}
}

func TestScanCloudDetectsDuplicateLikeNamesButKeepsThem(t *testing.T) {
	fs := newFakeFS().addTree("/root",
		"paper.docx|10",
		"paper(1).docx|20",
		"paper(2).docx|30",
		"notdup(abc).docx|40",
	)
	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if len(scan.DuplicateLike) != 2 {
		t.Fatalf("DuplicateLike = %v, want 2 entries", scan.DuplicateLike)
	}
	// Reporting must not remove anything: all four files stay in the tree, so a
	// locally present "paper(1).docx" can still be matched and kept.
	if len(scan.Entries) != 4 {
		t.Fatalf("all entries must remain, got %d", len(scan.Entries))
	}
}

func TestScanCloudFlagsUnsafeNames(t *testing.T) {
	fs := newFakeFS().addTree("/root", "ok.txt|1", "bad:name.txt|2", "CON|3")
	scan, err := ScanCloud(fs, "/root", ScanLimits{})
	if err != nil {
		t.Fatalf("ScanCloud: %v", err)
	}
	if len(scan.UnsafeNames) != 2 {
		t.Fatalf("UnsafeNames = %v, want 2", scan.UnsafeNames)
	}
}

func TestStructuralDigestIsStableAndOrderIndependent(t *testing.T) {
	spec := []string{"a.txt|10", "dir/", "dir/b.txt|20"}
	fs1 := newFakeFS().addTree("/root", spec...)
	fs2 := newFakeFS().addTree("/root", "dir/", "dir/b.txt|20", "a.txt|10")

	s1, err := ScanCloud(fs1, "/root", ScanLimits{})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := ScanCloud(fs2, "/root", ScanLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if s1.StructuralDigest() != s2.StructuralDigest() {
		t.Fatalf("digest should not depend on visitation order: %s vs %s",
			s1.StructuralDigest(), s2.StructuralDigest())
	}

	fs3 := newFakeFS().addTree("/root", "a.txt|11", "dir/", "dir/b.txt|20")
	s3, err := ScanCloud(fs3, "/root", ScanLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if s1.StructuralDigest() == s3.StructuralDigest() {
		t.Fatal("digest must change when a file size changes")
	}
}

func TestRelPath(t *testing.T) {
	cases := []struct{ root, abs, want string }{
		{"/root", "/root", ""},
		{"/root", "/root/a", "a"},
		{"/root", "/root/a/b", "a/b"},
		{"/", "/a", "a"},
		{"/root/", "/root/a", "a"},
	}
	for _, c := range cases {
		if got := RelPath(c.root, c.abs); got != c.want {
			t.Errorf("RelPath(%q,%q) = %q, want %q", c.root, c.abs, got, c.want)
		}
	}
}

func TestProbeReportsIncompleteScanAndNeverClaimsSuccess(t *testing.T) {
	fs := newFakeFS().addTree("/root", "a.txt|1", "sub/", "sub/b.txt:2")
	fs.failList["/root/sub"] = fmt.Errorf("网络错误")

	report, err := RunProbe(fs, &UserInfo{Name: "tester"}, ProbeOptions{
		RemoteRoot: "/root", DiscoverRoots: false,
	})
	if err != nil {
		t.Fatalf("RunProbe: %v", err)
	}
	joined := strings.Join(report.Verdicts, " | ")
	if !strings.Contains(joined, "FAIL") {
		t.Fatalf("expected a FAIL verdict for an incomplete enumeration, got %q", joined)
	}
	if strings.Contains(joined, "PASS") {
		t.Fatalf("must not report PASS alongside FAIL, got %q", joined)
	}
}

func TestProbeDiscoverRootsMatchesOfficialNaming(t *testing.T) {
	fs := newFakeFS()
	fs.dirs["/"] = []RemoteEntry{
		{Path: "/我的资源", Name: "我的资源", IsDir: true},
		{Path: "/来自：本地电脑", Name: "来自：本地电脑", IsDir: true},
		{Path: "/文档", Name: "文档", IsDir: true},
	}
	fs.metas["/"] = RemoteEntry{Path: "/", IsDir: true}
	fs.dirs["/来自：本地电脑"] = []RemoteEntry{
		{Path: "/来自：本地电脑/WorkSync", Name: "WorkSync", IsDir: true},
	}
	fs.metas["/我的资源"] = RemoteEntry{Path: "/我的资源", IsDir: true}
	fs.metas["/来自：本地电脑"] = RemoteEntry{Path: "/来自：本地电脑", IsDir: true}
	fs.metas["/文档"] = RemoteEntry{Path: "/文档", IsDir: true}

	candidates, err := DiscoverBackupRoots(fs)
	if err != nil {
		t.Fatalf("DiscoverBackupRoots: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected the backup root to be discovered")
	}
	found := false
	for _, c := range candidates {
		if c.Path == "/来自：本地电脑" {
			found = true
			if c.Children != 1 {
				t.Errorf("expected 1 child, got %d", c.Children)
			}
		}
		// Discovery is advisory only and must never propose an action.
		if c.Path == "/文档" {
			t.Errorf("unrelated directory must not be reported as a backup root")
		}
	}
	if !found {
		t.Fatalf("expected /来自：本地电脑 in candidates, got %+v", candidates)
	}
}

// metasErr is a tiny helper so tests can assert on the fake's own view.
func (f *fakeFS) metasErr(path string) error {
	if _, ok := f.metas[NormalizeRemoteRoot(path)]; ok {
		return nil
	}
	return &NotFoundError{Path: path}
}
