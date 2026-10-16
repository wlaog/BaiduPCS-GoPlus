package reconcile

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
)

func md5hex(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) }

func TestSinglePassHashesAndMutation(t *testing.T) {
	for _, size := range []int{0, 1, 256*1024 + 1, int(baidupcs.MinUploadBlockSize) + 19} {
		data := bytes.Repeat([]byte("a"), size)
		name := filepath.Join(t.TempDir(), "内容.bin")
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
		var read int64
		h, err := hashFile(name, func(n int64) { read = n })
		if err != nil {
			t.Fatal(err)
		}
		if h.MD5 != md5hex(data) || read != int64(size) {
			t.Fatal("full checksum or bytes read mismatch")
		}
		slice := data
		if len(slice) > 256*1024 {
			slice = slice[:256*1024]
		}
		if h.SliceMD5 != md5hex(slice) {
			t.Fatal("slice checksum mismatch")
		}

		if size > 0 {
			changed := false
			_, err = hashFile(name, func(n int64) {
				if !changed {
					changed = true
					_ = os.Chtimes(name, time.Now(), time.Now().Add(time.Hour))
				}
			})
			if err == nil {
				t.Fatal("mutation while hashing must be rejected")
			}
		}
	}
}

func TestHashCacheStrictAndQuickModes(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "a.txt")
	_ = os.WriteFile(name, []byte("old"), 0600)
	fs := newFakeFS().addTree("/backup", "a.txt|3|"+md5hex([]byte("old")))
	cacheFile := filepath.Join(t.TempDir(), "cache.json")
	cache := LoadHashCache(cacheFile)
	opt := PlanOptions{LocalRoot: root, RemoteRoot: "/backup", HashCache: cache}
	plan, err := BuildPlan(fs, opt)
	if err != nil || len(plan.Keeps) != 1 {
		t.Fatalf("initial plan: %v", err)
	}
	if err = cache.Save(cacheFile); err != nil {
		t.Fatal(err)
	}
	if err = cache.Save(cacheFile); err != nil {
		t.Fatal("replace existing cache:", err)
	}
	opt.HashCache = LoadHashCache(cacheFile)
	opt.UseHashCache = true
	var read atomic.Int64
	opt.Progress = func(p Progress) {
		if p.Phase == "hash" {
			read.Store(p.Bytes)
		}
	}
	plan, err = BuildPlan(fs, opt)
	if err != nil || len(plan.Keeps) != 1 || read.Load() != 0 {
		t.Fatal("quick mode must reuse persisted hashes")
	}
	st, _ := os.Stat(name)
	_ = os.WriteFile(name, []byte("new"), 0600)
	_ = os.Chtimes(name, st.ModTime(), st.ModTime())
	opt.UseHashCache = false
	plan, err = BuildPlan(fs, opt)
	if err != nil || len(plan.Pending) != 1 {
		t.Fatal("strict scan must detect same-size, preserved-mtime changes")
	}
}

func TestFullyScannedRemovalSubtreeCountsAndRevalidation(t *testing.T) {
	fs := newFakeFS().addTree("/backup", "old/a.txt|8", "old/deep/b.txt|9")
	scan, err := ScanCloud(fs, "/backup", ScanLimits{})
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{cloudSnapshot: scan.Entries, Removes: []PlannedAction{{Kind: ActionRemove, RemotePath: "/backup/old", IsDir: true, RemovedFiles: 2, Size: 17}}}
	if plan.RemoveFiles() != 2 || plan.RemoveBytes() != 17 {
		t.Fatal("directory cleanup thresholds must include all contained files")
	}
	current := make(map[string]RemoteEntry)
	for p, e := range plan.cloudSnapshot {
		current[p] = e
	}
	if !removalsUnchanged(plan, current) {
		t.Fatal("unchanged tree rejected")
	}
	current["/backup/old/new.txt"] = RemoteEntry{Path: "/backup/old/new.txt", Size: 10}
	if removalsUnchanged(plan, current) {
		t.Fatal("new descendant must block removal")
	}
}

type concurrentReader struct {
	mu sync.Mutex
	*fakeFS
	active    atomic.Int64
	maxActive atomic.Int64
}

func (r *concurrentReader) Meta(path string) (RemoteEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakeFS.Meta(path)
}
func (r *concurrentReader) ListDir(path string) ([]RemoteEntry, error) {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		old := r.maxActive.Load()
		if n <= old || r.maxActive.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakeFS.ListDir(path)
}
func TestParallelCloudScanMatchesSerialAndRetainsFailures(t *testing.T) {
	fs := newFakeFS().addTree("/backup", "a/x|1", "b/y|2", "c/z|3", "d/q|4", "e/p|5")
	serial, err := ScanCloud(fs, "/backup", ScanLimits{})
	if err != nil {
		t.Fatal(err)
	}
	r := &concurrentReader{fakeFS: fs}
	parallel, err := ScanCloud(r, "/backup", ScanLimits{Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if serial.StructuralDigest() != parallel.StructuralDigest() || !sameCloudTree(serial.Entries, parallel.Entries) || serial.Stats.ListingRequests != parallel.Stats.ListingRequests {
		t.Fatal("parallel scan must preserve authoritative results")
	}
	if r.maxActive.Load() != 4 {
		t.Fatal("expected four bounded directory workers")
	}
	fs.failList["/backup/c"] = fmt.Errorf("listing failed")
	broken, err := ScanCloud(r, "/backup", ScanLimits{Workers: 4})
	if err != nil || broken.ScanComplete() {
		t.Fatal("parallel failure must invalidate scan completeness")
	}
}

func TestMissingDirectoryIncludesInternalExcludedFilesAsOneUnit(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0600)
	fs := newFakeFS().addTree("/backup", "old/ignore.tmp|1", "old/delete.txt|2")
	plan, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removes) != 1 || plan.Removes[0].RelPath != "old" || !plan.Removes[0].WholeDir || fs.listCalls["/backup/old"] != 0 {
		t.Fatal("absent folder must be one opaque removal, including its internal temporary files")
	}
}

func BenchmarkPlanStrategies(b *testing.B) {
	for _, scenario := range []string{"unchanged", "small-change", "rename", "cloud-copy"} {
		for _, quick := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/quick=%v", scenario, quick), func(b *testing.B) {
				root := b.TempDir()
				fs := newFakeFS().addTree("/backup")
				data := bytes.Repeat([]byte("x"), 64*1024)
				for i := 0; i < 128; i++ {
					name := fmt.Sprintf("f%03d.bin", i)
					_ = os.WriteFile(filepath.Join(root, name), data, 0600)
					fs.addPath("/backup", fmt.Sprintf("%s|%d|%s", name, len(data), md5hex(data)))
				}
				cache := LoadHashCache(filepath.Join(b.TempDir(), "cache.json"))
				opt := PlanOptions{LocalRoot: root, RemoteRoot: "/backup", HashCache: cache}
				if _, err := BuildPlan(fs, opt); err != nil {
					b.Fatal(err)
				}
				switch scenario {
				case "small-change":
					_ = os.WriteFile(filepath.Join(root, "f000.bin"), bytes.Repeat([]byte("y"), len(data)), 0600)
				case "rename":
					_ = os.Rename(filepath.Join(root, "f000.bin"), filepath.Join(root, "renamed.bin"))
				case "cloud-copy":
					_ = os.WriteFile(filepath.Join(root, "f000.bin"), bytes.Repeat([]byte("y"), len(data)), 0600)
					fs.addPath("/backup", fmt.Sprintf("f000(1).bin|%d|%s", len(data), md5hex(bytes.Repeat([]byte("y"), len(data)))))
				}
				opt.UseHashCache = quick
				var read atomic.Int64
				opt.Progress = func(p Progress) {
					if p.Phase == "hash" {
						read.Store(p.Bytes)
					}
				}
				// Warm the changed-file cache too: measure repeat previews, not an
				// imaginary first scan that already knows new file contents.
				if _, err := BuildPlan(fs, opt); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				var readTotal int64
				for i := 0; i < b.N; i++ {
					read.Store(0)
					p, err := BuildPlan(fs, opt)
					if err != nil {
						b.Fatal(err)
					}
					readTotal += read.Load()
					if scenario == "unchanged" && !p.ZeroDifference() {
						b.Fatal("unexpected difference")
					}
				}
				b.ReportMetric(float64(readTotal)/float64(b.N), "hash-bytes/op")
			})
		}
	}
}
