package reconcile

import (
	"fmt"
	"strings"
)

// fakeFS is an in-memory Reader used by unit tests. It lets the planning and
// scanning logic be exercised without a live Baidu account, and lets tests
// reproduce server misbehaviour (partial listings, missing paths, network
// failures) that a real account cannot be asked to produce on demand.
type fakeFS struct {
	// dirs maps a directory path to its direct children.
	dirs map[string][]RemoteEntry
	// metas holds explicit metadata; a path missing here and not a directory
	// key is reported as NotFound.
	metas map[string]RemoteEntry
	// failList maps a directory path to the error its listing returns.
	failList map[string]error
	// failMeta maps a path to the error its metadata lookup returns.
	failMeta map[string]error
	// listCalls counts listing requests per path.
	listCalls map[string]int
	// metaCalls counts metadata requests per path.
	metaCalls map[string]int
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		dirs:      make(map[string][]RemoteEntry),
		metas:     make(map[string]RemoteEntry),
		failList:  make(map[string]error),
		failMeta:  make(map[string]error),
		listCalls: make(map[string]int),
		metaCalls: make(map[string]int),
	}
}

// fakeTree is a compact tree description. Each spec line is one path; missing
// intermediate directories are created automatically.
//
//	"dir/"                  a directory
//	"file"                  a zero-length file
//	"file|1234"             a file of the given size
//	"file|1234|md5hex"      a file with a pinned server-reported content hash
//
// The "|" separator is used rather than ":" so that a file name containing a
// colon (which is exactly the sort of hostile name some tests need) stays
// unambiguous.
func (f *fakeFS) addTree(root string, spec ...string) *fakeFS {
	root = NormalizeRemoteRoot(root)
	if _, ok := f.dirs[root]; !ok {
		f.dirs[root] = nil
	}
	f.metas[root] = RemoteEntry{Path: root, Name: baseName(root), IsDir: true}
	for _, line := range spec {
		f.addPath(root, line)
	}
	return f
}

func (f *fakeFS) addPath(root, line string) {
	// The "is a directory" signal is the trailing slash on the original spec
	// line. It must be read BEFORE trimming, because strings.Trim removes
	// trailing slashes and would leave "figures/" indistinguishable from a
	// zero-length file called "figures".
	wantDir := strings.HasSuffix(strings.TrimSpace(line), "/")
	rel := strings.Trim(line, "/")
	if rel == "" {
		return
	}
	parts := strings.Split(rel, "/")

	cur := root
	for i, raw := range parts {
		last := i == len(parts)-1
		if raw == "" {
			continue
		}

		name := raw
		size := int64(0)
		md5hex := ""
		isDir := !last || wantDir
		if !isDir {
			fields := strings.Split(raw, "|")
			name = fields[0]
			if len(fields) > 1 && fields[1] != "" {
				fmt.Sscanf(fields[1], "%d", &size)
			}
			if len(fields) > 2 {
				md5hex = fields[2]
			}
		}

		childPath := JoinRemote(cur, name)
		if !f.hasChild(cur, childPath) {
			entry := RemoteEntry{
				Path: childPath, Name: name, IsDir: isDir,
				Size: size, ContentMD5: md5hex,
				ContentMD5Trusted: md5hex != "",
				BlockCount:        boolToInt(md5hex != ""),
				Mtime:             1700000000,
			}
			f.dirs[cur] = append(f.dirs[cur], entry)
			if isDir {
				if _, ok := f.dirs[childPath]; !ok {
					f.dirs[childPath] = nil
				}
				f.metas[childPath] = entry
			} else {
				f.metas[childPath] = entry
			}
		}
		cur = childPath
	}
}

func (f *fakeFS) hasChild(dir, child string) bool {
	for _, e := range f.dirs[dir] {
		if e.Path == child {
			return true
		}
	}
	return false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (f *fakeFS) ListDir(remotePath string) ([]RemoteEntry, error) {
	p := NormalizeRemoteRoot(remotePath)
	f.listCalls[p]++
	if err, ok := f.failList[p]; ok {
		return nil, err
	}
	children, ok := f.dirs[p]
	if !ok {
		return nil, &NotFoundError{Path: p}
	}
	out := append([]RemoteEntry(nil), children...)
	sortRemoteEntries(out)
	return out, nil
}

func (f *fakeFS) Meta(remotePath string) (RemoteEntry, error) {
	p := NormalizeRemoteRoot(remotePath)
	f.metaCalls[p]++
	if err, ok := f.failMeta[p]; ok {
		return RemoteEntry{}, err
	}
	if m, ok := f.metas[p]; ok {
		return m, nil
	}
	return RemoteEntry{}, &NotFoundError{Path: p}
}

// setChildren replaces the children of a directory, letting a test simulate a
// server that returns an arbitrary (possibly truncated) page.
func (f *fakeFS) setChildren(dir string, children ...RemoteEntry) *fakeFS {
	f.dirs[NormalizeRemoteRoot(dir)] = children
	return f
}

// makeSiblings builds n file entries named file-0000 .. file-<n-1>.
func makeSiblings(parent string, n int) []RemoteEntry {
	out := make([]RemoteEntry, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file-%04d.txt", i)
		out = append(out, RemoteEntry{
			Path: JoinRemote(parent, name), Name: name, Size: int64(i), Mtime: 1700000000,
		})
	}
	return out
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
