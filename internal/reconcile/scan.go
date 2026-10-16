package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// ScanLimits bounds a remote enumeration so a runaway or unexpectedly huge
// backup tree cannot turn a probe into an unbounded walk.
type ScanLimits struct {
	Workers  int
	Progress ProgressFunc
	// PruneDir deliberately treats selected directories as opaque units. Only
	// cleanup planning uses this; probes still enumerate the entire tree.
	PruneDir func(RemoteEntry) bool
	// MaxEntries caps the total number of entries recorded. Zero means the
	// default (200000).
	MaxEntries int
	// MaxDepth is the depth at which enumeration stops. Zero means the default
	// safety backstop (64).
	MaxDepth int
	// ExpectedMaxDepth is the depth the caller deliberately asked for. When the
	// stop depth is at least this deep, the stop is the caller's own bound and
	// bounds the report without invalidating it. When the stop depth is
	// shallower, the safety backstop fired early and data below the stop point
	// was never seen, which IS a completeness failure. Zero means "no requested
	// bound", so any depth stop is treated as a failure.
	ExpectedMaxDepth int
}

// DefaultScanLimits returns the limits used when the caller does not care.
func DefaultScanLimits() ScanLimits {
	return ScanLimits{MaxEntries: 200000, MaxDepth: 64}
}

func (l ScanLimits) withDefaults() ScanLimits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = 200000
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = 64
	}
	return l
}

// DuplicateNamePattern matches the "(n)" duplicate suffix the official backup
// client produces when it re-uploads a file it believes is new.
//
// This is REPORTING ONLY. Reconcile never deletes a cloud file merely because
// its name matches this pattern: a locally present "paper(1).docx" is a real
// file and must be kept. Removal decisions are driven purely by absence from
// the local tree.
var DuplicateNamePattern = regexp.MustCompile(`^(.*)\((\d+)\)(\.[^.]*)?$`)

// NotFoundFilenames are the placeholder names the cloud client uses to keep an
// otherwise-empty directory visible. They are not real user data.
var NotFoundFilenames = map[string]bool{
	".bzEmpty": true,
}

// ScanStats carries the integrity signals collected during a scan.
type ScanStats struct {
	// DirCount and FileCount count scanned entries (excluding the root itself).
	DirCount  int
	FileCount int
	// TotalBytes is the sum of file sizes.
	TotalBytes int64
	// MaxDepth is the deepest directory level reached (root children are 1).
	MaxDepth int
	// ListingRequests counts cloud list calls made.
	ListingRequests int
	// LargeDirs lists directories spanning at least one full page (1000).
	// This is informational; the adapter returns pagination faults as errors.
	LargeDirs map[string]int
	// Errors records subtrees that could not be enumerated. Any entry here is
	// fatal for planning: an unreadable subtree must never look "empty".
	Errors []ScanError
	// DepthBounded lists directories where enumeration stopped because the
	// caller asked for a shallower scan. This bounds the report without making
	// it wrong, so it is informational rather than fatal.
	DepthBounded []string
	// Truncated reports that a scan limit stopped enumeration early.
	Truncated   bool
	TruncatedAt string
	// AuthFailed reports that the session became invalid mid-scan. When set,
	// the scan is not merely incomplete: nothing after that point was verified.
	AuthFailed bool
	// AuthError carries the message that triggered AuthFailed.
	AuthError string
}

// ScanError is one failed directory enumeration.
type ScanError struct {
	Path string
	Err  string
}

// PanicPageSize mirrors baidupcs' per-page listing size. A directory returning
// exactly this many children may have been truncated by the pagination bug.
const PanicPageSize = 1000

// CloudScan is a full enumeration of the cloud root plus the integrity signals
// needed to decide whether that enumeration can be trusted.
type CloudScan struct {
	Root       string
	PrunedDirs map[string]bool
	// Entries are all descendants of Root, keyed by cloud path. Root itself is
	// not included.
	Entries map[string]RemoteEntry
	// Order preserves a deterministic listing order.
	Order []string
	// Dirs are the cloud directories seen (path -> true), Root excluded.
	Dirs map[string]bool
	// UnsafeNames collects entries whose names would be problematic to map
	// onto a local filesystem (Windows-illegal characters, trailing dots or
	// spaces, control characters, reserved device names, path separators).
	UnsafeNames map[string]string
	// DuplicateLike counts "(n)"-suffixed files, for reporting only.
	DuplicateLike []string
	Stats         ScanStats
}

// StructuralDigest is a stable fingerprint of the scanned tree's shape: every
// path with its type and size, but no timestamps or hashes. Two consecutive
// dry-runs that produce the same digest agree on what exists.
func (s *CloudScan) StructuralDigest() string {
	keys := make([]string, 0, len(s.Entries))
	for k := range s.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		e := s.Entries[k]
		kind := "F"
		if e.IsDir {
			kind = "D"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\n", kind, RelPath(s.Root, k), e.Size, e.BlockCount)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ScanComplete reports whether the enumeration can be trusted as a full picture
// of the tree under Root.
//
// A scan that stopped only because the caller asked for a shallower depth is
// still "complete" for what it was asked to do: it recorded every entry within
// the requested bound. It is NOT complete when a subtree could not be listed,
// when the safety backstop fired, or when an entry/depth limit cut enumeration
// short without the caller asking for it.
func (s *CloudScan) ScanComplete() bool {
	return len(s.Stats.Errors) == 0
}

// ShortOfRequestedDepth reports that the scan stopped early for a reason other
// than the caller's requested depth, i.e. data below the stop point was never
// seen even though the caller wanted it.
func (s *CloudScan) ShortOfRequestedDepth() bool {
	return s.Stats.Truncated && len(s.Stats.Errors) > 0
}

// RelPath converts an absolute cloud path into a "/"-separated path relative to
// root. It returns "" for the root itself.
func RelPath(root, abs string) string {
	root = NormalizeRemoteRoot(root)
	abs = NormalizeRemoteRoot(abs)
	if abs == root {
		return ""
	}
	prefix := root
	if prefix != "/" {
		prefix += "/"
	} else {
		prefix = "/"
	}
	rel := strings.TrimPrefix(abs, prefix)
	return strings.Trim(rel, "/")
}

// ScanCloud enumerates the whole cloud tree under root.
//
// It never returns an error for a merely-unreachable subtree: failures are
// recorded in Stats.Errors so the caller can decide, and so a caller can never
// mistake "failed to list" for "nothing is there".
func ScanCloud(r Reader, root string, limits ScanLimits) (*CloudScan, error) {
	if r == nil {
		return nil, ErrNoClient
	}
	limits = limits.withDefaults()
	root = NormalizeRemoteRoot(root)

	scan := &CloudScan{
		Root:        root,
		PrunedDirs:  make(map[string]bool),
		Entries:     make(map[string]RemoteEntry),
		Dirs:        make(map[string]bool),
		UnsafeNames: make(map[string]string),
		Stats:       ScanStats{LargeDirs: make(map[string]int)},
	}

	// Confirm the root exists and is a directory before walking it. Without
	// this, a typo in --remote would produce an empty scan that looks like
	// "the cloud copy is empty, upload everything".
	limits.Progress.emit(Progress{Phase: "cloud", Current: root})
	meta, err := r.Meta(root)
	if err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("%w: 云端根目录不存在: %s", ErrUsage, root)
		}
		return nil, err
	}
	if !meta.IsDir {
		return nil, fmt.Errorf("%w: 云端根目录不是目录: %s", ErrUsage, root)
	}

	type queueItem struct {
		path  string
		depth int
	}
	queue := []queueItem{{path: root, depth: 0}}
	type listed struct {
		entries []RemoteEntry
		err     error
	}
	prefetched := make(map[string]listed)
	workers := limits.Workers
	if workers <= 0 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}

	for len(queue) > 0 {
		if len(prefetched) == 0 {
			n := workers
			if n > len(queue) {
				n = len(queue)
			}
			results := make([]listed, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				if queue[i].depth >= limits.MaxDepth {
					continue
				}
				scan.Stats.ListingRequests++
				wg.Add(1)
				go func(i int, p string) {
					defer wg.Done()
					defer func() {
						if recover() != nil {
							results[i].err = fmt.Errorf("目录扫描异常中止")
						}
					}()
					results[i].entries, results[i].err = r.ListDir(p)
				}(i, queue[i].path)
			}
			limits.Progress.emit(Progress{Phase: "cloud", Current: queue[0].path, Files: scan.Stats.FileCount, Dirs: scan.Stats.DirCount, Bytes: scan.Stats.TotalBytes})
			wg.Wait()
			for i := 0; i < n; i++ {
				prefetched[queue[i].path] = results[i]
			}
		}
		cur := queue[0]
		queue = queue[1:]
		result := prefetched[cur.path]
		delete(prefetched, cur.path)
		limits.Progress.emit(Progress{Phase: "cloud", Current: cur.path, Done: int64(scan.Stats.ListingRequests), Files: scan.Stats.FileCount, Dirs: scan.Stats.DirCount, Bytes: scan.Stats.TotalBytes})

		if cur.depth >= limits.MaxDepth {
			// Distinguish "the caller asked to stop here" from "the safety
			// backstop fired". Stopping at the requested depth bounds the
			// report without invalidating it. Stopping at the backstop while
			// the caller wanted to go at least this deep means real data below
			// was never seen, so nothing there may be planned for removal.
			scan.Stats.Truncated = true
			scan.Stats.TruncatedAt = cur.path
			// Enumeration stops at depth MaxDepth. The backstop fired only when
			// that cap is SHALLOWER than the depth the caller asked for;
			// otherwise the stop is the caller's own requested bound.
			backstopFired := limits.ExpectedMaxDepth <= 0 || limits.MaxDepth < limits.ExpectedMaxDepth
			if backstopFired {
				scan.Stats.Errors = append(scan.Stats.Errors, ScanError{
					Path: cur.path,
					Err:  fmt.Sprintf("达到安全深度上限 %d, 未继续枚举", limits.MaxDepth),
				})
			} else {
				scan.Stats.DepthBounded = append(scan.Stats.DepthBounded, cur.path)
			}
			continue
		}

		children, listErr := result.entries, result.err
		if listErr != nil {
			// An authentication failure is not a local gap: the session died and
			// every subsequent listing would fail too. Continuing would record
			// the rest of the tree as effectively empty, which the planner would
			// then read as "everything below is locally deleted". Abort instead.
			if IsAuthError(listErr) {
				scan.Stats.Errors = append(scan.Stats.Errors, ScanError{Path: cur.path, Err: listErr.Error()})
				scan.Stats.AuthFailed = true
				scan.Stats.AuthError = listErr.Error()
				return scan, &AuthExpiredError{Cause: listErr, At: cur.path}
			}
			// Record and continue: a sibling subtree may still be scannable,
			// and the caller blocks any destructive step when Errors is
			// non-empty.
			scan.Stats.Errors = append(scan.Stats.Errors, ScanError{Path: cur.path, Err: listErr.Error()})
			continue
		}

		sortRemoteEntries(children)
		if len(children) >= PanicPageSize {
			scan.Stats.LargeDirs[cur.path] = len(children)
		}

		for _, child := range children {
			if NotFoundFilenames[child.Name] {
				continue
			}
			if len(scan.Entries) >= limits.MaxEntries {
				scan.Stats.Truncated = true
				scan.Stats.TruncatedAt = child.Path
				// Running out of entry budget is never something the caller
				// asked for, so it must invalidate completeness rather than
				// quietly produce a partial tree.
				scan.Stats.Errors = append(scan.Stats.Errors, ScanError{
					Path: cur.path,
					Err:  fmt.Sprintf("条目数达到上限 %d, 未继续枚举", limits.MaxEntries),
				})
				break
			}
			if strings.TrimSpace(child.Path) == "" {
				// Defensive: a server entry without a path cannot be mapped.
				scan.Stats.Errors = append(scan.Stats.Errors, ScanError{
					Path: cur.path,
					Err:  fmt.Sprintf("云端返回了缺少 path 的条目 (server_filename=%q)", child.Name),
				})
				continue
			}
			if ParentRemote(child.Path) != cur.path || child.Name != path.Base(child.Path) {
				scan.Stats.Errors = append(scan.Stats.Errors, ScanError{Path: cur.path, Err: "云端条目路径越界或名称不一致"})
				continue
			}

			scan.Entries[child.Path] = child
			scan.Order = append(scan.Order, child.Path)

			if reason := UnsafeNameReason(child.Name); reason != "" {
				scan.UnsafeNames[child.Path] = reason
			}
			if !child.IsDir && DuplicateNamePattern.MatchString(child.Name) {
				scan.DuplicateLike = append(scan.DuplicateLike, child.Path)
			}

			if child.IsDir {
				scan.Dirs[child.Path] = true
				scan.Stats.DirCount++
				if cur.depth+1 > scan.Stats.MaxDepth {
					scan.Stats.MaxDepth = cur.depth + 1
				}
				if limits.PruneDir != nil && limits.PruneDir(child) {
					scan.PrunedDirs[child.Path] = true
				} else {
					queue = append(queue, queueItem{path: child.Path, depth: cur.depth + 1})
				}
			} else {
				scan.Stats.FileCount++
				scan.Stats.TotalBytes += child.Size
			}
		}
		if scan.Stats.Truncated {
			break
		}
	}

	sort.Strings(scan.DuplicateLike)
	return scan, nil
}

// windowsReservedNames are device names that cannot be used as file names on
// Windows even with an extension.
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// UnsafeNameReason returns a non-empty explanation when a cloud entry name
// cannot be represented faithfully as a local file name on Windows. Such
// entries are never auto-removed: they are surfaced as conflicts, because the
// name the cloud shows may not be the name the official client uses locally.
func UnsafeNameReason(name string) string {
	if name == "" {
		return "名称为空"
	}
	if name != path.Base(name) || strings.ContainsAny(name, `/\`) {
		return "名称包含路径分隔符"
	}
	if name == "." || name == ".." {
		return "名称为相对路径标记"
	}
	for _, r := range name {
		switch {
		case r < 32:
			return "名称含控制字符"
		case strings.ContainsRune(`<>:"|?*`, r):
			return fmt.Sprintf("名称含 Windows 非法字符 %q", r)
		}
		if unicode.Is(unicode.Cf, r) {
			return "名称含不可见格式字符"
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "名称以点或空格结尾 (Windows 无法表示)"
	}
	base := name
	if idx := strings.Index(base, "."); idx >= 0 {
		base = base[:idx]
	}
	if windowsReservedNames[strings.ToUpper(base)] {
		return fmt.Sprintf("名称 %q 是 Windows 保留设备名", base)
	}
	return ""
}

// IsSubPath reports whether child is root itself or lies strictly beneath it.
// It is the guard that keeps every action inside the caller-declared root.
func IsSubPath(root, child string) bool {
	root = NormalizeRemoteRoot(root)
	child = NormalizeRemoteRoot(child)
	if root == "/" {
		return true
	}
	if child == root {
		return true
	}
	return strings.HasPrefix(child, root+"/")
}

// assertInsideRoot is used by every mutating path in this package.
func assertInsideRoot(root, target string) error {
	if root == "" || target == "" {
		return fmt.Errorf("%w: 云端根目录或目标路径为空", ErrUsage)
	}
	if !IsSubPath(root, target) {
		return fmt.Errorf("%w: 目标 %s 不在云端根目录 %s 之内, 已拒绝", ErrUsage, target, root)
	}
	return nil
}
