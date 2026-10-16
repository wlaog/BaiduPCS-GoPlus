package reconcile

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
)

func removeRemoteScope(entries map[string]RemoteEntry, scope string) {
	for p := range entries {
		if IsSubPath(scope, p) {
			delete(entries, p)
		}
	}
}

// RetryFailedScan reuses successful local/cloud snapshots and content hashes.
// Only the failed scopes are read again. Reports alone are not executable state.
func RetryFailedScan(r Reader, previous *Plan, progress ProgressFunc) (*Plan, error) {
	if r == nil {
		return nil, ErrNoClient
	}
	if previous == nil || previous.localSnapshot == nil || previous.cloudScan == nil {
		return nil, fmt.Errorf("当前服务没有可复用的扫描结果，请重新预览")
	}
	if previous.localSnapshot.Stats.Truncated || previous.cloudScan.Stats.Truncated {
		return nil, fmt.Errorf("扫描达到安全上限，需要调整范围后重新预览")
	}
	if err := checkLocalRoot(previous); err != nil {
		return nil, err
	}
	opt := previous.planOptions
	opt.Progress = progress
	local := *previous.localSnapshot
	local.Entries = make(map[string]LocalEntry, len(previous.localSnapshot.Entries))
	for p, e := range previous.localSnapshot.Entries {
		local.Entries[p] = e
	}
	local.Errors = nil
	for _, scope := range minimalScopes(previous.localSnapshot.Errors) {
		absolute := filepath.Join(local.Root, filepath.FromSlash(scope))
		info, err := os.Lstat(absolute)
		if err != nil && !os.IsNotExist(err) {
			local.Errors = append(local.Errors, ScanError{Path: scope, Err: err.Error()})
			continue
		}
		if err == nil && !info.IsDir() && !info.Mode().IsRegular() {
			local.Errors = append(local.Errors, ScanError{Path: scope, Err: "路径当前不是普通文件或目录"})
			continue
		}
		var sub *LocalScanResult
		if err == nil && info.IsDir() {
			sub, err = ScanLocal(absolute, LocalScanOptions{Progress: progress, SkipSuffixes: opt.SkipSuffixes, SkipNames: opt.SkipNames, SkipPrefixes: opt.SkipPrefixes})
			if err != nil {
				local.Errors = append(local.Errors, ScanError{Path: scope, Err: err.Error()})
				continue
			}
			if sub.Stats.Truncated {
				local.Errors = append(local.Errors, ScanError{Path: scope, Err: "补扫达到本地条目上限"})
				continue
			}
		}
		for p := range local.Entries {
			if relContains(scope, p) {
				delete(local.Entries, p)
			}
		}
		if info == nil {
			continue
		} // confirmed absent after the original failure
		local.Entries[scope] = LocalEntry{RelPath: scope, AbsPath: absolute, Name: info.Name(), Size: info.Size(), IsDir: info.IsDir(), Mtime: info.ModTime().Unix(), MtimeNano: info.ModTime().UnixNano(), Sparse: localNameUnsafe(info.Name())}
		if sub != nil {
			for rel, e := range sub.Entries {
				e.RelPath = joinRel(scope, rel)
				local.Entries[e.RelPath] = e
			}
			for _, e := range sub.Errors {
				local.Errors = append(local.Errors, ScanError{Path: joinRel(scope, e.Path), Err: e.Err})
			}
		}
	}
	local.Order = nil
	local.Stats.FileCount, local.Stats.DirCount, local.Stats.TotalBytes = 0, 0, 0
	for rel, e := range local.Entries {
		local.Order = append(local.Order, rel)
		if e.IsDir {
			local.Stats.DirCount++
		} else {
			local.Stats.FileCount++
			local.Stats.TotalBytes += e.Size
		}
	}
	sort.Strings(local.Order)

	cloud := *previous.cloudScan
	cloud.Entries = make(map[string]RemoteEntry, len(previous.cloudSnapshot))
	for p, e := range previous.cloudSnapshot {
		cloud.Entries[p] = e
	}
	cloud.PrunedDirs = make(map[string]bool)
	for p := range previous.cloudScan.PrunedDirs {
		if _, exists := cloud.Entries[p]; exists {
			cloud.PrunedDirs[p] = true
		}
	}
	cloud.Stats.Errors = nil
	cloud.Stats.ListingRequests = 0
	limits := cleanupScanLimits(&local, opt)
	for _, scope := range minimalScopes(previous.cloudScan.Stats.Errors) {
		progress.emit(Progress{Phase: "cloud", Current: scope})
		meta, err := r.Meta(scope)
		if scope == cloud.Root && (err != nil || !meta.IsDir) {
			cloud.Stats.Errors = append(cloud.Stats.Errors, ScanError{Path: scope, Err: "云端根目录当前不可访问或不是目录"})
			continue
		}
		if err != nil && !IsNotFound(err) {
			cloud.Stats.Errors = append(cloud.Stats.Errors, ScanError{Path: scope, Err: err.Error()})
			continue
		}
		if err != nil {
			removeRemoteScope(cloud.Entries, scope)
			delete(cloud.PrunedDirs, scope)
			continue
		}
		if !meta.IsDir {
			removeRemoteScope(cloud.Entries, scope)
			cloud.Entries[scope] = meta
			delete(cloud.PrunedDirs, scope)
			continue
		}
		if scope != cloud.Root && limits.PruneDir(meta) {
			removeRemoteScope(cloud.Entries, scope)
			cloud.Entries[scope] = meta
			cloud.PrunedDirs[scope] = true
			continue
		}
		sub, err := ScanCloud(r, scope, limits)
		if err != nil {
			cloud.Stats.Errors = append(cloud.Stats.Errors, ScanError{Path: scope, Err: err.Error()})
			continue
		}
		seen := make(map[string]bool)
		for _, p := range sub.Order {
			if seen[p] || !IsSubPath(scope, p) {
				sub.Stats.Errors = append(sub.Stats.Errors, ScanError{Path: scope, Err: "补扫返回重复或越界路径，此范围继续保护"})
				break
			}
			seen[p] = true
		}
		removeRemoteScope(cloud.Entries, scope)
		if scope != cloud.Root {
			cloud.Entries[scope] = meta
		}
		for p, e := range sub.Entries {
			cloud.Entries[p] = e
		}
		for p := range cloud.PrunedDirs {
			if IsSubPath(scope, p) {
				delete(cloud.PrunedDirs, p)
			}
		}
		for p := range sub.PrunedDirs {
			cloud.PrunedDirs[p] = true
		}
		cloud.Stats.Errors = append(cloud.Stats.Errors, sub.Stats.Errors...)
		cloud.Stats.ListingRequests += sub.Stats.ListingRequests
		if sub.Stats.Truncated {
			cloud.Stats.Truncated = true
		}
	}
	cloud.Order = nil
	cloud.Dirs = make(map[string]bool)
	cloud.UnsafeNames = make(map[string]string)
	cloud.Stats.FileCount, cloud.Stats.DirCount, cloud.Stats.TotalBytes = 0, 0, 0
	for p, e := range cloud.Entries {
		cloud.Order = append(cloud.Order, p)
		if e.IsDir {
			cloud.Dirs[p] = true
			cloud.Stats.DirCount++
		} else {
			cloud.Stats.FileCount++
			cloud.Stats.TotalBytes += e.Size
		}
		if reason := UnsafeNameReason(e.Name); reason != "" {
			cloud.UnsafeNames[p] = reason
		}
	}
	sort.Strings(cloud.Order)
	return assemblePlan(&local, &cloud, opt, previous.hashSnapshot), nil
}

func minimalScopes(errors []ScanError) []string {
	paths := make([]string, 0, len(errors))
	for _, e := range errors {
		paths = append(paths, e.Path)
	}
	sort.Strings(paths)
	var scopes []string
	for _, p := range paths {
		covered := false
		for _, ancestor := range scopes {
			if relContains(ancestor, p) {
				covered = true
				break
			}
		}
		if !covered {
			scopes = append(scopes, p)
		}
	}
	return scopes
}
func joinRel(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return path.Join(a, b)
}
