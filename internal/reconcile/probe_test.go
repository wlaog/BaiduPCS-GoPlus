package reconcile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderProbeHumanDoesNotPanicAndHidesSecrets(t *testing.T) {
	fs := newFakeFS().addTree("/来自：本地电脑/WorkSync",
		"paper.docx|1000|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"paper(1).docx|2000|bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"deleted_old.txt|30|cccccccccccccccccccccccccccccccc",
		"figures/",
		"figures/plot.png|2000|dddddddddddddddddddddddddddddddd",
		"bad:name.txt|5|eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	)
	fs.dirs["/"] = []RemoteEntry{
		{Path: "/来自：本地电脑", Name: "来自：本地电脑", IsDir: true},
	}
	fs.metas["/"] = RemoteEntry{Path: "/", IsDir: true}
	fs.dirs["/来自：本地电脑"] = []RemoteEntry{
		{Path: "/来自：本地电脑/WorkSync", Name: "WorkSync", IsDir: true},
	}
	fs.metas["/来自：本地电脑"] = RemoteEntry{Path: "/来自：本地电脑", IsDir: true}
	fs.failList["/来自：本地电脑/WorkSync/figures"] = &RemoteError{
		Op: "列出云端目录", Path: "/来自：本地电脑/WorkSync/figures", Msg: "网络错误",
	}

	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "figures"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel string, data string) {
		if err := os.WriteFile(filepath.Join(local, filepath.FromSlash(rel)), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("paper.docx", strings.Repeat("A", 1000))
	write("notes.md", "notes")
	write("figures/plot.png", strings.Repeat("B", 2000))

	report, err := RunProbe(fs, &UserInfo{Name: "tester"}, ProbeOptions{
		RemoteRoot:    "/来自：本地电脑/WorkSync",
		LocalDir:      local,
		DiscoverRoots: true,
		HashSamples:   3,
	})
	if err != nil {
		t.Fatalf("RunProbe: %v", err)
	}

	var buf bytes.Buffer
	RenderProbeHuman(&buf, report)
	out := buf.String()

	for _, want := range []string{
		"可行性探测报告",
		"/来自：本地电脑/WorkSync",
		"结构指纹",
		"枚举失败",
		"疑似官方副本",
		"无法在 Windows 忠实映射的名称",
		"判定",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("human report is missing %q\n---\n%s", want, out)
		}
	}
	// The incomplete subtree must produce a FAIL verdict, and "PASS: 云端目录可完整枚举"
	// must not also appear, or the operator could read the run as healthy.
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected a FAIL verdict for the unreachable subtree:\n%s", out)
	}
	if strings.Contains(out, "PASS: 云端目录可完整枚举") {
		t.Errorf("report must not claim a complete enumeration:\n%s", out)
	}
	// The report must never carry credential material.
	for _, secret := range []string{"BDUSS", "STOKEN", "PTOKEN", "SBOXTKN", "BAIDUID"} {
		if strings.Contains(out, secret) {
			t.Errorf("human report leaked %s", secret)
		}
	}

	// The (n)-suffixed cloud file must be reported but never scheduled for
	// removal merely because of its name.
	if len(report.Scan.DuplicateLike) != 1 {
		t.Errorf("expected paper(1).docx to be reported as duplicate-like, got %v", report.Scan.DuplicateLike)
	}
	if _, ok := report.Scan.Entries["/来自：本地电脑/WorkSync/paper(1).docx"]; !ok {
		t.Error("the duplicate-like file must remain in the tree, not be dropped")
	}

	// A local file absent from the cloud must appear as an UPLOAD candidate.
	foundLocalOnly := false
	for _, e := range report.LocalOnlySample {
		if e.RelPath == "notes.md" {
			foundLocalOnly = true
		}
	}
	if !foundLocalOnly {
		t.Errorf("expected notes.md in the local-only sample, got %+v", report.LocalOnlySample)
	}

	// The JSON report must round-trip.
	jsonOut, jErr := report.ProbeJSON()
	if jErr != nil {
		t.Fatalf("ProbeJSON: %v", jErr)
	}
	if len(jsonOut) == 0 {
		t.Fatal("empty JSON report")
	}
	dir := t.TempDir()
	path, wErr := WriteProbeReport(dir, report)
	if wErr != nil {
		t.Fatalf("WriteProbeReport: %v", wErr)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("report file not written: %v", statErr)
	}
}

func TestRedactHidesAccountButKeepsPaths(t *testing.T) {
	fs := newFakeFS().addTree("/root", "a.txt|10|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"dir/", "dir/b.txt|20|bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	report, err := RunProbe(fs, &UserInfo{Name: "zhangsan", UID: 12345678}, ProbeOptions{
		RemoteRoot: "/root",
		Redact:     true,
	})
	if err != nil {
		t.Fatalf("RunProbe: %v", err)
	}
	if strings.Contains(report.AccountName, "zhangsan") {
		t.Errorf("redaction must hide the account name, got %q", report.AccountName)
	}
	if report.ServerUser != "" {
		t.Errorf("redaction must clear the server user field, got %q", report.ServerUser)
	}
	// Paths are the substance of the report and must survive redaction.
	if _, ok := report.Scan.Entries["/root/dir/b.txt"]; !ok {
		t.Error("redaction must not remove cloud paths")
	}

	// Without redaction the name is preserved, so the operator can tell reports
	// from different accounts apart.
	plain, err := RunProbe(fs, &UserInfo{Name: "zhangsan"}, ProbeOptions{RemoteRoot: "/root"})
	if err != nil {
		t.Fatalf("RunProbe: %v", err)
	}
	if plain.AccountName != "zhangsan" {
		t.Errorf("unredacted report should carry the account name, got %q", plain.AccountName)
	}
}

func TestLocalContentHashMatchesKnownVectors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := LocalContentHash(empty)
	if err != nil {
		t.Fatalf("LocalContentHash: %v", err)
	}
	// A zero-byte file must hash to the MD5 of the empty input. Upstream's
	// checksum.GetFileSum returns nil here, because its write path returns
	// early for an empty file without ever calling Sum().
	if h.MD5 != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("empty file MD5 = %q, want d41d8cd98f00b204e9800998ecf8427e", h.MD5)
	}
	if h.SliceMD5 != h.MD5 {
		t.Errorf("for an empty file the slice MD5 must equal the file MD5, got %q", h.SliceMD5)
	}
	if h.Size != 0 {
		t.Errorf("Size = %d, want 0", h.Size)
	}

	abc := filepath.Join(dir, "abc.bin")
	if err := os.WriteFile(abc, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := LocalContentHash(abc)
	if err != nil {
		t.Fatalf("LocalContentHash: %v", err)
	}
	if h2.MD5 != "900150983cd24fb0d6963f7d28e17f72" {
		t.Errorf("abc MD5 = %s, want 900150983cd24fb0d6963f7d28e17f72", h2.MD5)
	}
	if h2.SliceMD5 != h2.MD5 {
		t.Errorf("below the slice size the slice MD5 must equal the file MD5")
	}
	if h2.Size != 3 {
		t.Errorf("Size = %d, want 3", h2.Size)
	}
}

// Above the 256 KiB slice threshold the full-file hash and the leading-slice
// hash must differ, which is what makes the distinction worth reporting.
func TestLocalContentHashSeparatesFullAndSliceAboveThreshold(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	data := make([]byte, 300*1024)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := os.WriteFile(big, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := LocalContentHash(big)
	if err != nil {
		t.Fatalf("LocalContentHash: %v", err)
	}
	if h.MD5 == h.SliceMD5 {
		t.Error("for a file larger than the slice size the two hashes must differ")
	}
	if h.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", h.Size, len(data))
	}
}

func TestScanLocalBuildsNormalisedTree(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, data string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("paper.docx", "aaa")
	mustWrite("figures/plot.png", "bbbb")
	mustWrite("论文/中文 名称 (1).md", "cc")
	mustWrite("notes.md.tmp", "transient")

	res, err := ScanLocal(dir, LocalScanOptions{})
	if err != nil {
		t.Fatalf("ScanLocal: %v", err)
	}
	for _, want := range []string{"paper.docx", "figures/plot.png", "论文/中文 名称 (1).md"} {
		if _, ok := res.Entries[want]; !ok {
			t.Errorf("missing %q in %v", want, res.Order)
		}
	}
	// Relative paths must always use forward slashes, on Windows too.
	for rel := range res.Entries {
		if strings.Contains(rel, `\`) {
			t.Errorf("relative path %q must not contain a backslash", rel)
		}
	}
	if _, ok := res.Entries["notes.md.tmp"]; ok {
		t.Error("transient .tmp file should be skipped by default")
	}
	if res.Stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", res.Stats.Skipped)
	}
	if res.Stats.FileCount != 3 {
		t.Errorf("FileCount = %d, want 3", res.Stats.FileCount)
	}
}

func TestScanLocalRejectsFileAndMissingRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanLocal(file, LocalScanOptions{}); err == nil {
		t.Error("expected an error when the local root is a file")
	}
	if _, err := ScanLocal(filepath.Join(dir, "nope"), LocalScanOptions{}); err == nil {
		t.Error("expected an error when the local root does not exist")
	}
	if _, err := ScanLocal("", LocalScanOptions{}); err == nil {
		t.Error("expected an error for an empty local root")
	}
}
