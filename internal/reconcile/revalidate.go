package reconcile

import "strings"

func sameRemote(a, b RemoteEntry) bool {
	return a.IsDir == b.IsDir && a.Size == b.Size && (a.FsID == 0 || a.FsID == b.FsID) && a.Mtime == b.Mtime && strings.EqualFold(a.ContentMD5, b.ContentMD5)
}

func sameCloudTree(a, b map[string]RemoteEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for path, expected := range a {
		actual, ok := b[path]
		if !ok || !sameRemote(expected, actual) {
			return false
		}
	}
	return true
}

func removalsUnchanged(plan *Plan, current map[string]RemoteEntry) bool {
	roots := make(map[string]bool, len(plan.Removes))
	for _, a := range plan.Removes {
		roots[a.RemotePath] = true
	}
	subset := func(tree map[string]RemoteEntry) map[string]RemoteEntry {
		out := make(map[string]RemoteEntry)
		for path, entry := range tree {
			for p := path; p != "/"; p = ParentRemote(p) {
				if roots[p] {
					out[path] = entry
					break
				}
			}
		}
		return out
	}
	return sameCloudTree(subset(plan.cloudSnapshot), subset(current))
}

func matchesHash(entry RemoteEntry, h *CloudContentHash, size int64) bool {
	return !entry.IsDir && entry.Size == size && h != nil && entry.ContentMD5Trusted && strings.EqualFold(entry.ContentMD5, h.MD5)
}
