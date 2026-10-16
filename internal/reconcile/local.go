package reconcile

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocalEntry is one file or directory under the local root, reduced to what the
// planner needs. AbsPath is kept for hashing and for operator messages, but it
// is never used to modify anything.
type LocalEntry struct {
	// RelPath is the "/"-separated path relative to the local root.
	RelPath string
	// AbsPath is the absolute local path.
	AbsPath string
	// Name is the base name, as reported by the OS.
	Name string
	// Size is the file size in bytes.
	Size int64
	// IsDir reports whether this is a directory.
	IsDir bool
	// Mtime is the modification time in Unix seconds.
	Mtime     int64
	MtimeNano int64
	// Sparse reports an entry that exists in the cloud but cannot be
	// represented faithfully on Windows (illegal name, over-long path, ...).
	// These are never removed and never uploaded.
	Sparse string
}

// LocalScanOptions tunes the local walk.
type LocalScanOptions struct {
	Progress ProgressFunc
	// SkipNames are file/directory names skipped anywhere in the tree.
	SkipNames []string
	// SkipSuffixes are filename suffixes skipped anywhere in the tree.
	SkipSuffixes []string
	// SkipPrefixes are filename prefixes skipped anywhere in the tree.
	SkipPrefixes []string
	// MaxEntries caps the walk. Zero means the default (500000).
	MaxEntries int
	// FlagEditing marks files modified within this duration as "possibly being
	// written right now" (mtime-based). Zero means the default (2 minutes).
	FlagEditing time.Duration
	// FollowLinks enables descending into symlinks/junctions. Default false,
	// which is the safe behaviour: a link out of the root would silently widen
	// the mirrored set, and a link loop would never terminate.
	FollowLinks bool
}

// DefaultSkipSuffixes are transient artefacts that should never be treated as
// authoritative content. Excluding them is reported, and the cloud counterparts
// of excluded names are never auto-removed unless explicitly authorised.
func DefaultSkipSuffixes() []string {
	return []string{
		".tmp", ".temp", ".crdownload", ".partial", ".part",
		".swp", ".swx", "~", ".lock", ".bak.tmp",
	}
}

// DefaultSkipPrefixes are transient name prefixes. "~$" is what Office uses for
// the lock file it keeps beside an open document; it is never user content.
func DefaultSkipPrefixes() []string {
	return []string{"~$"}
}

// DefaultSkipNames are file names that are never treated as mirror content.
//
// desktop.ini is created by Windows to hold folder view settings. It exists on
// the local side and, once the official client has backed it up, on the cloud
// side too; without this it is reported as "cloud is missing it" and "cloud has
// an extra copy" at the same time, which is noise at best and a wrong removal
// decision at worst.
func DefaultSkipNames() []string {
	return []string{"desktop.ini", "thumbs.db", ".ds_store"}
}

// LocalScanStats summarises the local walk.
type LocalScanStats struct {
	FileCount   int
	DirCount    int
	TotalBytes  int64
	Skipped     int
	ReparseSeen int
	Truncated   bool
}

// LocalScanResult is the full local tree.
type LocalScanResult struct {
	Root    string
	Entries map[string]LocalEntry // keyed by RelPath
	Order   []string
	Stats   LocalScanStats
	// Warnings are non-fatal observations for the operator.
	Warnings []string
}

// CloudContentHash is the content hash the reconciler computes for comparison.
type CloudContentHash struct {
	// MD5 is the full-file MD5 as a lowercase hex string.
	MD5        string
	MtimeNano  int64
	ChunkSize  int64
	BlocksList []string
	// SliceMD5 is the leading-256KiB checksum required by rapid upload.
	SliceMD5 string
	// Size is the file size used for the hash.
	Size int64
	// Mtime is the modification time observed when hashing started.
	Mtime int64
}

// LocalContentHash computes the full MD5 and the leading-256KB MD5 of a local
// file, along with protocol block hashes for a possible subsequent upload.
func LocalContentHash(localPath string) (CloudContentHash, error) {
	return hashFile(localPath, nil)
}

// ScanLocal walks the local root and builds a normalised entry map.
//
// Rules that matter for safety:
//   - The root must exist and be a readable directory.
//   - Symlinks, junctions and other reparse points are recorded but not
//     descended into unless FollowLinks is set, so a link cannot pull files
//     from outside the root into the mirror.
//   - Only regular files and directories are mirrored. Sockets, devices and
//     other special files are skipped with a warning.
func ScanLocal(root string, opt LocalScanOptions) (*LocalScanResult, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: 本地根目录为空", ErrUsage)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: 无法解析本地路径 %q: %v", ErrUsage, root, err)
	}
	abs = filepath.Clean(abs)
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = 500000
	}
	if opt.FlagEditing <= 0 {
		opt.FlagEditing = 2 * time.Minute
	}
	if opt.SkipSuffixes == nil {
		opt.SkipSuffixes = DefaultSkipSuffixes()
	}
	if opt.SkipPrefixes == nil {
		opt.SkipPrefixes = DefaultSkipPrefixes()
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: 本地目录不可访问 %s: %v", ErrUsage, abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: 本地路径不是目录: %s", ErrUsage, abs)
	}

	res := &LocalScanResult{
		Root:    abs,
		Entries: make(map[string]LocalEntry),
	}

	type queueItem struct{ abs, rel string }
	queue := []queueItem{{abs: abs, rel: ""}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		opt.Progress.emit(Progress{Phase: "local", Current: cur.rel, Files: res.Stats.FileCount, Dirs: res.Stats.DirCount, Bytes: res.Stats.TotalBytes})
		dirEntries, readErr := os.ReadDir(cur.abs)
		if readErr != nil {
			// An unreadable local directory is fatal: treating it as empty
			// would mark its entire cloud subtree as redundant.
			return nil, fmt.Errorf("%w: 读取本地目录失败 %s: %v", ErrUsage, cur.abs, readErr)
		}
		sort.Slice(dirEntries, func(i, j int) bool { return dirEntries[i].Name() < dirEntries[j].Name() })

		for _, de := range dirEntries {
			if (res.Stats.FileCount+res.Stats.DirCount)%128 == 0 {
				opt.Progress.emit(Progress{Phase: "local", Current: cur.rel, Files: res.Stats.FileCount, Dirs: res.Stats.DirCount, Bytes: res.Stats.TotalBytes})
			}
			if len(res.Entries) >= opt.MaxEntries {
				res.Stats.Truncated = true
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("本地条目数超过上限 %d, 已停止遍历于 %s", opt.MaxEntries, cur.rel))
				break
			}
			name := de.Name()
			childAbs := filepath.Join(cur.abs, name)
			rel := name
			if cur.rel != "" {
				rel = cur.rel + "/" + name
			}

			if shouldSkipLocal(name, opt) {
				res.Stats.Skipped++
				continue
			}
			sparse := localNameUnsafe(name)
			// Local byte length says nothing about the cloud path's validity:
			// Chinese UTF-8 paths routinely exceed 250 bytes while remaining
			// short Windows paths. Let filesystem calls report actual access
			// errors; cloud paths are built independently from relative paths.

			// Lstat: do not follow links while deciding the type.
			li, lErr := os.Lstat(childAbs)
			if lErr != nil {
				return nil, fmt.Errorf("本地条目无法读取状态，扫描不完整: %s: %w", rel, lErr)
			}
			isLink := li.Mode()&os.ModeSymlink != 0
			if isLink {
				res.Stats.ReparseSeen++
			}

			if li.IsDir() || (isLink && opt.FollowLinks && isDirFollowing(childAbs)) {
				res.Entries[rel] = LocalEntry{
					RelPath: rel, AbsPath: childAbs, Name: name,
					IsDir: true, Mtime: li.ModTime().Unix(), MtimeNano: li.ModTime().UnixNano(), Sparse: sparse,
				}
				res.Order = append(res.Order, rel)
				res.Stats.DirCount++
				if isLink && !opt.FollowLinks {
					continue
				}
				if isLink {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("跟随符号链接/联接点进入 %s (--follow-links 已启用)", rel))
				}
				queue = append(queue, queueItem{abs: childAbs, rel: rel})
				continue
			}

			if isLink && !opt.FollowLinks {
				// A link to a file: mirror its target's content only if that
				// target is inside the root; otherwise skip and warn.
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("跳过符号链接/联接点 %s (默认不跟随, 如需跟随请显式启用)", rel))
				res.Stats.Skipped++
				continue
			}
			if !li.Mode().IsRegular() {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("跳过非普通文件 %s (%s)", rel, li.Mode().String()))
				res.Stats.Skipped++
				continue
			}

			res.Entries[rel] = LocalEntry{
				RelPath: rel, AbsPath: childAbs, Name: name,
				Size: li.Size(), Mtime: li.ModTime().Unix(), MtimeNano: li.ModTime().UnixNano(), Sparse: sparse,
			}
			res.Order = append(res.Order, rel)
			res.Stats.FileCount++
			res.Stats.TotalBytes += li.Size()
		}
		if res.Stats.Truncated {
			break
		}
	}

	sort.Strings(res.Order)
	return res, nil
}

func isDirFollowing(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// shouldSkipLocal reports whether an exclusion rule covers a name.
//
// It applies the defaults itself rather than relying on the caller, because an
// empty SkipSuffixes slice is NOT harmless: strings.HasSuffix(x, "") is always
// true, so a single empty entry would silently exclude every file.
func shouldSkipLocal(name string, opt LocalScanOptions) bool {
	names := opt.SkipNames
	if len(names) == 0 {
		names = DefaultSkipNames()
	}
	for _, s := range names {
		if s != "" && strings.EqualFold(name, s) {
			return true
		}
	}
	prefixes := opt.SkipPrefixes
	if len(prefixes) == 0 {
		prefixes = DefaultSkipPrefixes()
	}
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	suffixes := opt.SkipSuffixes
	if len(suffixes) == 0 {
		suffixes = DefaultSkipSuffixes()
	}
	lower := strings.ToLower(name)
	for _, s := range suffixes {
		if s == "" {
			continue
		}
		if s == "~" {
			if strings.HasSuffix(name, "~") {
				return true
			}
			continue
		}
		if strings.HasSuffix(lower, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// localNameUnsafe reports why a local name cannot be mapped onto a cloud path,
// or "" when it is fine. Cloud paths are "/"-separated, so a backslash or the
// literal name "." is a mapping hazard.
func localNameUnsafe(name string) string {
	if name == "" {
		return "名称为空"
	}
	if name == "." || name == ".." {
		return "名称为相对路径标记"
	}
	if strings.ContainsAny(name, `/\`) {
		return "名称包含路径分隔符"
	}
	for _, r := range name {
		if r < 32 {
			return "名称含控制字符"
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "名称以点或空格结尾 (云端不保留, 无法忠实映射)"
	}
	return ""
}

// IsEditing reports whether a local file looks like it may be mid-write.
func IsEditing(e LocalEntry, now time.Time, window time.Duration) bool {
	if e.IsDir || e.Mtime == 0 {
		return false
	}
	return now.Sub(time.Unix(e.Mtime, 0)) < window
}

// CheckLocalUnchanged re-checks that a local file still has the size and mtime
// observed during the scan. If the file changed after planning, its cloud
// versions must be preserved until a new preview confirms the content.
func CheckLocalUnchanged(e LocalEntry) error {
	info, err := os.Stat(e.AbsPath)
	if err != nil {
		return fmt.Errorf("本地文件在计划生成后已不可访问: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("本地路径在计划生成后变成了目录: %s", e.RelPath)
	}
	if info.Size() != e.Size {
		return fmt.Errorf("本地文件在计划生成后大小已改变 (%d -> %d), 可能正在写入: %s",
			e.Size, info.Size(), e.RelPath)
	}
	if info.ModTime().Unix() != e.Mtime || (e.MtimeNano != 0 && info.ModTime().UnixNano() != e.MtimeNano) {
		return fmt.Errorf("本地文件在计划生成后修改时间已改变, 可能正在编辑: %s", e.RelPath)
	}
	return nil
}

// PrefixRel returns the first path element of a relative cloud path.
func PrefixRel(rel string) string {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return ""
	}
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
}

// ParentRel returns the "/"-separated parent of a relative path, or "" when
// there is no parent (at the root, or one level down).
//
// It must return "" rather than "." for a top-level path: callers walk parents
// in a loop, and a "." result would never terminate.
func ParentRel(rel string) string {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return ""
	}
	dir := path.Dir(rel)
	if dir == "." || dir == "/" || dir == rel {
		return ""
	}
	return strings.Trim(dir, "/")
}
