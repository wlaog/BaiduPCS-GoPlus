// Package reconcile implements a one-way, manually triggered "make the cloud
// backup directory match the local directory" mirror for BaiduPCS-Go.
//
// Design constraints (see README_BaiduPCS_Backup_Reconcile.md):
//   - The local directory is the only source of truth; nothing ever writes to it.
//   - Cloud changes are only ever made inside one caller-supplied remote root.
//   - Default mode is dry-run; destructive steps need explicit confirmation.
//   - Every cloud removal is a move into a quarantine area, never a hard delete.
//
// This file defines the abstraction the rest of the package is written against,
// so the planning logic can be unit-tested with an in-memory fake and never
// needs a live Baidu account.
package reconcile

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
)

// RemoteEntry is one file or directory on the cloud side, reduced to the fields
// this package actually reasons about.
type RemoteEntry struct {
	// Path is the absolute cloud path, e.g. "/测试备份/WorkSync/paper.docx".
	Path string
	// Name is the last path element as reported by the server.
	Name string
	// Size is the file size in bytes; 0 for directories.
	Size int64
	// IsDir reports whether this is a directory.
	IsDir bool
	// FsID is the stable server-side file id (used for de-duplication only).
	FsID int64
	// Mtime is the modification time in seconds since the Unix epoch, as
	// reported by the server. Zero means unknown.
	Mtime int64
	// ContentMD5 is the server-reported content hash as returned by the list or
	// meta API, normalised to lowercase.
	//
	// Measured against a live account, this is the FULL-file MD5 and is correct
	// for multi-block files too. It is still not treated as infallible: a match
	// is used as proof of equality and a mismatch only causes a re-upload, so a
	// stale server value can cost a redundant upload but never a missed update.
	ContentMD5 string
	// ContentMD5Trusted reports whether ContentMD5 is a syntactically valid
	// content hash and therefore usable for the equality test above.
	ContentMD5Trusted bool
	// BlockCount is len(block_list) as returned by the server; the list API
	// returns none, so this is usually 0 and is informational only.
	BlockCount int
}

// Reader is the read-only slice of the cloud API that this package needs. It is
// deliberately narrow: the planning half of reconcile cannot mutate anything,
// because it is physically unable to reach a mutating method.
//
// *baidupcs.BaiduPCS satisfies Reader via RemoteFS.
type Reader interface {
	// ListDir returns the direct children of an absolute cloud directory path.
	// It must return an empty slice (not an error) for an empty directory.
	ListDir(remotePath string) ([]RemoteEntry, error)
	// Meta returns the metadata for one absolute cloud path.
	Meta(remotePath string) (RemoteEntry, error)
}

// RemoteFS adapts the upstream *baidupcs.BaiduPCS client to the Reader
// interface. It is the only place in this package that knows about baidupcs.
type RemoteFS struct {
	PCS *baidupcs.BaiduPCS
	// Retry controls backoff for the provider's transient failures. The zero
	// value means DefaultRetryPolicy.
	Retry RetryPolicy
	// Sleep is the backoff sleep, injected so tests need not wait. Nil means
	// time.Sleep.
	Sleep func(time.Duration)
	// Log receives one line per retry. Nil disables logging.
	Log func(string)

	// lastRequest serialises pacing across calls. Only touched from the scan
	// goroutine, which walks directories sequentially.
	lastRequest time.Time
}

// pace enforces at most one request per Retry.Spacing. It is a no-op while
// requests are already spaced naturally, so a healthy scan is not slowed down.
func (r *RemoteFS) pace() {
	policy := r.Retry.withDefaults()
	if policy.Spacing <= 0 {
		return
	}
	if !r.lastRequest.IsZero() {
		if wait := policy.Spacing - time.Since(r.lastRequest); wait > 0 {
			r.sleep(wait)
		}
	}
	r.lastRequest = time.Now()
}

// NewRemoteFS wraps an authenticated upstream client.
func NewRemoteFS(pcs *baidupcs.BaiduPCS) *RemoteFS {
	return &RemoteFS{PCS: pcs}
}

func (r *RemoteFS) sleep(d time.Duration) {
	if r.Sleep != nil {
		r.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (r *RemoteFS) logf(format string, a ...interface{}) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, a...))
	}
}

// ListDir implements Reader.
//
// It intentionally calls FilesDirectoriesList (no cache) rather than
// CacheFilesDirectoriesList: the cached variant has a one-minute TTL and an
// upstream cache-key mismatch, so a reconcile run must never plan against it.
//
// Every request is spaced and retried, because Baidu throttles sustained
// listings and reports the throttling as an HTTP-level EOF or as an
// authentication-looking errno. A retried-out listing is returned as an error,
// never as an empty directory.
func (r *RemoteFS) ListDir(remotePath string) ([]RemoteEntry, error) {
	if r == nil || r.PCS == nil {
		return nil, ErrNoClient
	}
	policy := r.Retry.withDefaults()

	var list baidupcs.FileDirectoryList
	err, attempts, exhausted := withRetry(policy, r.sleep, r.pace, func() error {
		var pcsErr pcserror.Error
		list, pcsErr = r.PCS.FilesDirectoriesList(remotePath, baidupcs.DefaultOrderOptions)
		if pcsErr != nil {
			return wrapPCSError("列出云端目录", remotePath, pcsErr)
		}
		return nil
	})
	if err != nil {
		if attempts > 1 {
			r.logf("列出 %s 重试 %d 次后仍失败: %v", remotePath, attempts, err)
		}
		// Only a failure that survived every attempt is treated as a session
		// problem; a single -6 under load is just throttling. Returning a
		// distinct type makes the scanner abort instead of recording the
		// subtree as unreadable-and-therefore-empty.
		if exhausted && IsAuthError(err) {
			return nil, &AuthExpiredError{At: remotePath, Cause: err}
		}
		return nil, err
	}
	if attempts > 1 {
		r.logf("列出 %s 在第 %d 次尝试后成功 (限流重试)", remotePath, attempts)
	}

	out := make([]RemoteEntry, 0, len(list))
	for _, fd := range list {
		if fd == nil {
			continue
		}
		out = append(out, convertFileDirectory(fd))
	}
	sortRemoteEntries(out)
	return out, nil
}

// Meta implements Reader. It shares the retry policy with ListDir.
func (r *RemoteFS) Meta(remotePath string) (RemoteEntry, error) {
	if r == nil || r.PCS == nil {
		return RemoteEntry{}, ErrNoClient
	}
	policy := r.Retry.withDefaults()

	var fd *baidupcs.FileDirectory
	err, _, exhausted := withRetry(policy, r.sleep, r.pace, func() error {
		var pcsErr pcserror.Error
		fd, pcsErr = r.PCS.FilesDirectoriesMeta(remotePath)
		if pcsErr != nil {
			return wrapPCSError("获取云端元信息", remotePath, pcsErr)
		}
		return nil
	})
	if err != nil {
		if exhausted && IsAuthError(err) {
			return RemoteEntry{}, &AuthExpiredError{At: remotePath, Cause: err}
		}
		return RemoteEntry{}, err
	}
	if fd == nil {
		return RemoteEntry{}, &NotFoundError{Path: remotePath}
	}
	return convertFileDirectory(fd), nil
}

func convertFileDirectory(fd *baidupcs.FileDirectory) RemoteEntry {
	md5 := strings.ToLower(strings.TrimSpace(fd.MD5))
	return RemoteEntry{
		Path:              fd.Path,
		Name:              fd.Filename,
		Size:              fd.Size,
		IsDir:             fd.Isdir,
		FsID:              fd.FsID,
		Mtime:             fd.Mtime,
		ContentMD5:        md5,
		ContentMD5Trusted: looksLikeMD5(md5),
		BlockCount:        len(fd.BlockList),
	}
}

// looksLikeMD5 reports whether a server-reported hash is a usable content hash.
//
// Measured on a live account against files uploaded by the official client, the
// md5 returned by `/api/list` is the FULL-file content MD5, correct for both a
// 10-byte file and a 1 MiB multi-block file. The list API returns no block_list
// at all, so requiring a single block (as upstream's own display logic does)
// would discard a working equality proof and re-upload identical files.
//
// Trusting it is safe in the direction this package uses it: a hash match is
// taken as proof of equality and a mismatch only triggers a re-upload. A
// server-side stale md5 (upstream warns these can occur after chunked uploads)
// therefore costs at most a redundant upload, never a missed update.
func looksLikeMD5(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func sortRemoteEntries(entries []RemoteEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
}

// JoinRemote joins an absolute cloud root with a "/"-separated relative path.
// Cloud paths always use forward slashes, on every platform, so path.Join
// semantics (not filepath) are what we want.
func JoinRemote(root, rel string) string {
	root = NormalizeRemoteRoot(root)
	if rel == "" || rel == "." {
		return root
	}
	rel = strings.Trim(strings.ReplaceAll(rel, "\\", "/"), "/")
	if rel == "" {
		return root
	}
	if root == "/" {
		return "/" + rel
	}
	return root + "/" + rel
}

// NormalizeRemoteRoot makes a user-supplied cloud path absolute and free of
// trailing separators. It does not validate safety; see ValidateRemoteRoot.
func NormalizeRemoteRoot(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		return "/"
	}
	return p
}
