package reconcile

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ActionKind is the classification of one path in the reconcile plan.
type ActionKind string

const (
	ActionRename     ActionKind = "RENAME"
	ActionPending    ActionKind = "WAIT"
	ActionUnverified ActionKind = "UNVERIFIED"
	// ActionUpload means the path exists locally but not on the cloud.
	ActionUpload ActionKind = "UPLOAD"
	// ActionReplace means the path exists on both sides but the cloud copy is
	// not demonstrably identical to the local one.
	ActionReplace ActionKind = "REPLACE"
	// ActionRemove means the path exists on the cloud but not locally, so it is
	// not part of the current local tree.
	ActionRemove ActionKind = "REMOVE"
	// ActionMkdir means a cloud directory must be created before an upload.
	ActionMkdir ActionKind = "MKDIR"
	// ActionKeep means both sides agree.
	ActionKeep ActionKind = "KEEP"
	// ActionConflict means reconcile refuses to decide. Conflicts are never
	// acted on automatically.
	ActionConflict ActionKind = "CONFLICT"
)

// ComparisonRule records WHY a file pair was judged equal or unequal. Keeping
// the reason makes a plan auditable instead of a bare list of verbs.
type ComparisonRule string

const (
	// RuleHashMatch: a trustworthy server hash equals the locally computed hash.
	RuleHashMatch ComparisonRule = "完整内容哈希一致"
	// RuleHashMismatch: a trustworthy server hash differs.
	RuleHashMismatch ComparisonRule = "完整内容哈希不同"
	// RuleHashUnavailable: no usable content hash was reported.
	RuleHashUnavailable ComparisonRule = "服务端内容哈希缺失或不可用"
	// RuleSizeDiffers: sizes differ, which alone proves the content differs.
	RuleSizeDiffers ComparisonRule = "大小不同"
	// RuleSizeOnlyEqual: sizes match but no dependable hash was available.
	RuleSizeOnlyEqual ComparisonRule = "仅大小相同(无法证明内容一致)"
	// RuleMtimeNewer: sizes match and the local copy is newer.
	RuleMtimeNewer ComparisonRule = "大小相同且本地较新"
)

// PlannedAction is one decided operation. Nothing in this file executes it.
type PlannedAction struct {
	SourcePath string
	// Requires is a canonical local version that must be present before an
	// older duplicate can be removed.
	Requires string
	Kind     ActionKind
	// RelPath is the "/"-separated path relative to both roots ("" for the root
	// itself).
	RelPath string
	// RemotePath is the absolute cloud path this action concerns.
	RemotePath string
	// LocalPath is the corresponding local version, also used by history cleanup.
	LocalPath string
	// LocalSize and LocalMtime are the local file's size and mtime as observed
	// during the scan. The executor re-checks them before renaming or
	// removing history; comparing against a fresh snapshot would be vacuous.
	LocalSize      int64
	LocalMtime     int64
	LocalMtimeNano int64
	// Hash is retained in memory for this plan, never written into reports.
	Hash         *CloudContentHash `json:"-"`
	ReuseFrom    string
	RemovedFiles int
	// Size is the byte count of the content involved.
	Size int64
	// IsDir reports that the path is a directory.
	IsDir bool
	// WholeDir means descendants were deliberately not enumerated. The folder
	// and everything beneath it are removed as one explicitly previewed unit.
	WholeDir bool
	// Rule records the comparison outcome for file pairs.
	Rule ComparisonRule
	// Reason is the human-readable explanation shown in the preview.
	Reason string
	// Blocked marks an action the executor must not perform because a
	// precondition failed (for example the local file changed since the scan).
	Blocked bool
	// Stable records whether the local file was quiet long enough to be
	// considered settled. An unstable action is still planned, but the executor
	// MUST re-verify the local snapshot before changing its cloud versions.
	Stable bool
}

// PlanOptions configures plan generation.
type PlanOptions struct {
	Progress     ProgressFunc
	HashCache    *HashCache
	UseHashCache bool
	HashWorkers  int
	// LocalRoot is the authoritative local directory.
	LocalRoot string
	// RemoteRoot is the cloud directory to make match LocalRoot.
	RemoteRoot string
	// HashFiles retains the legacy front-end preference. Content comparisons
	// always require hashes; callers opt into cache reuse with UseHashCache.
	HashFiles bool
	// Conservative is a legacy input; cleanup always requires content evidence.
	Conservative bool
	// SkipSuffixes overrides the transient-file exclusions.
	SkipSuffixes []string
	// SkipNames overrides the excluded file/directory names.
	SkipNames []string
	// SkipPrefixes overrides the excluded name prefixes (Office lock files).
	SkipPrefixes []string
	// Now is the reference time for the editing check (test seam).
	Now time.Time
	// EditingWindow marks recently modified local files as possibly mid-write.
	EditingWindow time.Duration
	// Limits bounds the cloud enumeration.
	Limits ScanLimits
}

func (o PlanOptions) withDefaults() PlanOptions {
	return o
}

// Plan is the complete, reviewable difference between the two trees.
type Plan struct {
	Performance   PlanPerformance
	cloudSnapshot map[string]RemoteEntry
	localSnapshot *LocalScanResult
	cloudScan     *CloudScan
	planOptions   PlanOptions
	hashSnapshot  map[string]hashResult
	GeneratedAt   string
	LocalRoot     string
	RemoteRoot    string
	// StructuralDigest identifies the exact cloud snapshot this plan was built
	// from. Re-running a plan and getting the same digest means the cloud state
	// did not move.
	StructuralDigest string
	// UploadDirCount and the legacy write-action slices are retained only
	// to reject incompatible full-mirror plans. New plans leave them empty.
	UploadDirCount int

	Uploads         []PlannedAction
	Replaces        []PlannedAction
	Mkdirs          []PlannedAction
	Removes         []PlannedAction
	Keeps           []PlannedAction
	Conflicts       []PlannedAction
	Renames         []PlannedAction
	Pending         []PlannedAction
	Unverified      []PlannedAction
	ScanIssues      []ScanIssue
	PartialScanSafe bool

	// LocalFiles, LocalDirs, CloudFiles, CloudDirs describe the inputs.
	LocalFiles int
	LocalDirs  int
	CloudFiles int
	CloudDirs  int

	LocalBytes int64
	CloudBytes int64

	// ScanComplete reports full coverage, independently of whether scoped gaps
	// were safely isolated. Execution uses CanApply and per-action guards.
	ScanComplete bool
	// LocalScanComplete is false when the local walk hit its entry cap.
	LocalScanComplete bool
	// ScanErrors are the reasons completeness was lost.
	ScanErrors []string
	// Warnings are non-fatal observations.
	Warnings []string
	// SkipNotices explains what was excluded and what that means for removal.
	SkipNotices []string
}

type PlanPerformance struct {
	HashBytes     int64 `json:"hashBytes"`
	CacheHits     int   `json:"cacheHits"`
	CloudListings int   `json:"cloudListings"`
	ReuseFiles    int   `json:"reuseFiles"`
	ReuseBytes    int64 `json:"reuseBytes"`
	PrunedDirs    int   `json:"prunedDirs"`
}

// Changes reports how many actions would modify the cloud.
func (p *Plan) Changes() int {
	return len(p.Renames) + len(p.Removes)
}

// ZeroDifference reports whether the cloud already matches the local tree.
func (p *Plan) ZeroDifference() bool {
	return p.Changes() == 0 && len(p.Pending) == 0 && len(p.Conflicts) == 0 && len(p.ScanIssues) == 0
}

// HasConflicts reports whether any path needs human judgement.
func (p *Plan) HasConflicts() bool {
	return len(p.Conflicts) > 0
}

// UploadBytes is the total size of files to upload or replace.
func (p *Plan) UploadBytes() int64 {
	var n int64
	for _, a := range p.Uploads {
		n += a.Size
	}
	for _, a := range p.Replaces {
		n += a.Size
	}
	return n
}

// RemoveBytes is the total size of cloud files to remove.
func (p *Plan) RemoveBytes() int64 {
	var n int64
	for _, a := range p.Removes {
		n += a.Size
	}
	for _, a := range p.Renames {
		if old, ok := p.cloudSnapshot[a.RemotePath]; ok {
			n += old.Size
		}
	}
	return n
}

// RemoveFiles counts non-directory removals, which is what the blast-radius
// thresholds are expressed in.
func (p *Plan) RemoveFiles() int {
	n := 0
	for _, a := range p.Removes {
		if !a.IsDir {
			n++
		} else {
			n += a.RemovedFiles
		}
	}
	for _, a := range p.Renames {
		if _, ok := p.cloudSnapshot[a.RemotePath]; ok {
			n++
		}
	}
	return n
}

func (p *Plan) UnscannedRemoveDirs() int {
	n := 0
	for _, a := range p.Removes {
		if a.WholeDir {
			n++
		}
	}
	return n
}

// BuildPlan only plans recycle-bin removals and renames of confirmed versions.
// Missing local versions are left to the official backup client.
func BuildPlan(r Reader, opt PlanOptions) (*Plan, error) {
	if r == nil {
		return nil, ErrNoClient
	}
	if strings.TrimSpace(opt.LocalRoot) == "" || strings.TrimSpace(opt.RemoteRoot) == "" {
		return nil, fmt.Errorf("%w: 必须指定本地和云端目录", ErrUsage)
	}
	root := NormalizeRemoteRoot(opt.RemoteRoot)
	if root == "/" {
		return nil, fmt.Errorf("%w: 云端目录不能为根目录", ErrUsage)
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	local, err := ScanLocal(opt.LocalRoot, LocalScanOptions{Progress: opt.Progress, SkipSuffixes: opt.SkipSuffixes, SkipPrefixes: opt.SkipPrefixes, SkipNames: opt.SkipNames})
	if err != nil {
		return nil, err
	}
	opt.Limits = cleanupScanLimits(local, opt)
	scan, err := ScanCloud(r, root, opt.Limits)
	if err != nil {
		return nil, err
	}
	return assemblePlan(local, scan, opt, nil), nil
}

func cleanupScanLimits(local *LocalScanResult, opt PlanOptions) ScanLimits {
	root := NormalizeRemoteRoot(opt.RemoteRoot)
	limits := opt.Limits
	limits.Progress = opt.Progress
	limits.ContinueOnError = true
	limits.PruneDir = func(ce RemoteEntry) bool {
		rel := RelPath(root, ce.Path)
		for _, issue := range local.Errors {
			if relOverlaps(rel, issue.Path) {
				return false
			}
		}
		// Excluded paths and ambiguous local ancestors stay protected. For a
		// genuinely absent folder, child-level exclusions do not retain shells.
		for ancestor := rel; ancestor != ""; ancestor = ParentRel(ancestor) {
			if UnsafeNameReason(path.Base(ancestor)) != "" || shouldSkipLocal(path.Base(ancestor), LocalScanOptions{SkipSuffixes: opt.SkipSuffixes, SkipNames: opt.SkipNames, SkipPrefixes: opt.SkipPrefixes}) {
				return false
			}
			if le, ok := local.Entries[ancestor]; ok && (ancestor == rel || le.Sparse != "" || !le.IsDir) {
				return false
			}
		}
		_, err := os.Lstat(filepath.Join(local.Root, filepath.FromSlash(rel)))
		return os.IsNotExist(err)
	}
	return limits
}

func assemblePlan(local *LocalScanResult, scan *CloudScan, opt PlanOptions, hashes map[string]hashResult) *Plan {
	root := NormalizeRemoteRoot(opt.RemoteRoot)
	plan := &Plan{GeneratedAt: opt.Now.Format(time.RFC3339), LocalRoot: local.Root, RemoteRoot: root,
		LocalFiles: local.Stats.FileCount, LocalDirs: local.Stats.DirCount, LocalBytes: local.Stats.TotalBytes,
		CloudFiles: scan.Stats.FileCount, CloudDirs: scan.Stats.DirCount, CloudBytes: scan.Stats.TotalBytes,
		cloudSnapshot: scan.Entries, StructuralDigest: scan.StructuralDigest(),
		ScanComplete: scan.ScanComplete() && !scan.ShortOfRequestedDepth(), LocalScanComplete: !local.Stats.Truncated && len(local.Errors) == 0,
		Warnings: append([]string(nil), local.Warnings...)}
	plan.localSnapshot, plan.cloudScan, plan.planOptions = local, scan, opt
	plan.ScanIssues = scanIssues(local, scan)
	plan.PartialScanSafe = len(plan.ScanIssues) > 0 && !local.Stats.Truncated && !scan.Stats.Truncated
	for _, issue := range plan.ScanIssues {
		if !validIssueRel(issue.RelPath) {
			plan.PartialScanSafe = false
		}
		plan.Unverified = append(plan.Unverified, PlannedAction{Kind: ActionUnverified, RelPath: issue.RelPath, RemotePath: JoinRemote(root, issue.RelPath), Reason: issue.Side + "读取失败，保护此范围并继续其他项：" + issue.Reason})
	}
	plan.Performance.CloudListings = scan.Stats.ListingRequests
	plan.Performance.PrunedDirs = len(scan.PrunedDirs)
	if len(scan.PrunedDirs) > 0 {
		plan.SkipNotices = append(plan.SkipNotices, fmt.Sprintf("%d 个本地不存在的云端目录按整目录回收，不扫描内部文件；其全部内容（含临时文件）一并回收，内部数量与容量未统计", len(scan.PrunedDirs)))
	}
	seen := make(map[string]bool)
	for _, abs := range scan.Order {
		if seen[abs] || !IsSubPath(root, abs) {
			plan.ScanComplete = false
			plan.PartialScanSafe = false
			plan.ScanErrors = append(plan.ScanErrors, "云端路径重复或越界，扫描不可用于执行")
		}
		seen[abs] = true
	}
	for _, e := range scan.Stats.Errors {
		plan.ScanErrors = append(plan.ScanErrors, fmt.Sprintf("%s: %s", e.Path, e.Err))
	}
	for _, e := range local.Errors {
		plan.ScanErrors = append(plan.ScanErrors, "本地 "+e.Path+": "+e.Err)
	}
	p := &cleanupPlanner{plan: plan, local: local, scan: scan, opt: opt, versions: make(map[string][]RemoteEntry), owners: make(map[string]string), protected: make(map[string]bool), sources: make(map[string]bool), hashes: make(map[string]hashResult), dependencies: make(map[string]PlannedAction)}
	for _, issue := range plan.ScanIssues {
		p.protected[JoinRemote(root, issue.RelPath)] = true
	}
	for rel, h := range hashes {
		p.hashes[rel] = h
	}
	// Assign each suffix file to its nearest real local name. A real a(1).txt
	// takes precedence over a.txt and is never treated as its disposable copy.
	for _, abs := range scan.Order {
		ce := scan.Entries[abs]
		if ce.IsDir || p.excluded(ce) || scan.UnsafeNames[abs] != "" {
			continue
		}
		rel := RelPath(root, abs)
		if _, exists := local.Entries[rel]; exists {
			continue
		}
		name := ce.Name
		for {
			base, ok := stripVersionSuffix(name)
			if !ok {
				break
			}
			name = base
			candidate := JoinRemote(ParentRel(rel), name)
			candidate = strings.TrimPrefix(candidate, "/")
			if le, exists := local.Entries[candidate]; exists && !le.IsDir {
				p.versions[candidate] = append(p.versions[candidate], ce)
				p.owners[abs] = candidate
				break
			}
		}
	}
	p.prepareHashes()
	for _, rel := range local.Order {
		if plan.scopeUnverified(rel) {
			continue
		}
		p.classifyLocal(rel, local.Entries[rel])
	}
	p.classifyExtras()
	sortActions(plan.Renames)
	sortActions(plan.Removes)
	sortActions(plan.Pending)
	sortActions(plan.Keeps)
	sortActions(plan.Conflicts)
	plan.hashSnapshot = p.hashes
	opt.Progress.emit(Progress{Phase: "planned", Done: int64(local.Stats.FileCount), Total: int64(local.Stats.FileCount), Files: local.Stats.FileCount, Dirs: local.Stats.DirCount})
	return plan
}

type hashResult struct {
	hash   CloudContentHash
	err    error
	cached bool
}
type cleanupPlanner struct {
	plan               *Plan
	local              *LocalScanResult
	scan               *CloudScan
	opt                PlanOptions
	versions           map[string][]RemoteEntry
	owners             map[string]string
	protected, sources map[string]bool
	hashes             map[string]hashResult
	dependencies       map[string]PlannedAction
}

func stripVersionSuffix(name string) (string, bool) {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for _, pair := range [][2]string{{"(", ")"}, {"（", "）"}} {
		if !strings.HasSuffix(stem, pair[1]) {
			continue
		}
		i := strings.LastIndex(stem, pair[0])
		if i <= 0 {
			continue
		}
		digits := strings.TrimSuffix(stem[i+len(pair[0]):], pair[1])
		if digits == "" {
			continue
		}
		valid := false
		for _, c := range digits {
			if c < '0' || c > '9' {
				valid = false
				break
			}
			if c != '0' {
				valid = true
			}
		}
		if valid {
			base := strings.TrimRight(stem[:i], " ")
			if base != "" {
				return base + ext, true
			}
		}
	}
	return "", false
}

func (p *cleanupPlanner) prepareHashes() {
	var candidates []LocalEntry
	for _, rel := range p.local.Order {
		le := p.local.Entries[rel]
		if le.IsDir || le.Sparse != "" || p.plan.scopeUnverified(rel) {
			continue
		}
		if old, ok := p.hashes[rel]; ok && old.err == nil && CheckLocalUnchanged(le) == nil {
			p.plan.Performance.CacheHits++
			continue
		}
		entries := append([]RemoteEntry(nil), p.versions[rel]...)
		if ce, ok := p.scan.Entries[JoinRemote(p.scan.Root, rel)]; ok {
			entries = append(entries, ce)
		}
		for _, ce := range entries {
			if !ce.IsDir && ce.Size == le.Size && ce.ContentMD5Trusted && looksLikeMD5(ce.ContentMD5) {
				candidates = append(candidates, le)
				break
			}
		}
	}
	workers := p.opt.HashWorkers
	if workers <= 0 {
		workers = 2
	}
	if workers > 4 {
		workers = 4
	}
	jobs := make(chan LocalEntry)
	type result struct {
		rel string
		v   hashResult
	}
	results := make(chan result)
	var wg sync.WaitGroup
	var read, done atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for le := range jobs {
				var v hashResult
				info, err := os.Stat(le.AbsPath)
				if err == nil && p.opt.UseHashCache {
					v.hash, v.cached = p.opt.HashCache.get(le.AbsPath, info)
				}
				if err != nil {
					v.err = err
				} else if !v.cached {
					var previous int64
					v.hash, v.err = hashFile(le.AbsPath, func(n int64) {
						total := read.Add(n - previous)
						previous = n
						p.opt.Progress.emit(Progress{Phase: "hash", Current: le.RelPath, Done: done.Load(), Total: int64(len(candidates)), Bytes: total})
					})
				}
				if v.err == nil && (v.hash.Size != le.Size || v.hash.MtimeNano != le.MtimeNano) {
					v.err = fmt.Errorf("文件在扫描后发生变化")
				}
				if v.err == nil {
					p.opt.HashCache.put(le.AbsPath, v.hash)
				}
				results <- result{le.RelPath, v}
			}
		}()
	}
	go func() {
		for _, le := range candidates {
			jobs <- le
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	for res := range results {
		p.hashes[res.rel] = res.v
		if res.v.cached {
			p.plan.Performance.CacheHits++
		}
		n := done.Add(1)
		p.opt.Progress.emit(Progress{Phase: "hash", Current: res.rel, Done: n, Total: int64(len(candidates)), Bytes: read.Load()})
	}
	p.plan.Performance.HashBytes = read.Load()
	if p.opt.UseHashCache {
		p.plan.Warnings = append(p.plan.Warnings, "快速校验复用大小及修改时间未变的哈希；需要完整内容核对时请开启重新核对选项")
	}
}

func (p *cleanupPlanner) classifyLocal(rel string, le LocalEntry) {
	target := JoinRemote(p.scan.Root, rel)
	ce, exists := p.scan.Entries[target]
	a := PlannedAction{RelPath: rel, RemotePath: target, LocalPath: le.AbsPath, LocalSize: le.Size, LocalMtime: le.Mtime, LocalMtimeNano: le.MtimeNano, Size: le.Size, IsDir: le.IsDir, Stable: true}
	protect := func() {
		p.protected[target] = true
		for _, v := range p.versions[rel] {
			p.protected[v.Path] = true
		}
	}
	if le.Sparse != "" || exists && le.IsDir != ce.IsDir {
		a.Kind = ActionConflict
		a.Reason = "名称或文件/目录类型不匹配，跳过此项"
		p.plan.Conflicts = append(p.plan.Conflicts, a)
		protect()
		return
	}
	if le.IsDir {
		if !exists {
			a.Kind = ActionPending
			a.Reason = "等待官方备份创建目录"
			p.plan.Pending = append(p.plan.Pending, a)
		}
		return
	}
	v, hashed := p.hashes[rel]
	if hashed && v.err != nil {
		a.Kind = ActionConflict
		a.Reason = "无法稳定读取本地文件: " + v.err.Error()
		p.plan.Conflicts = append(p.plan.Conflicts, a)
		protect()
		return
	}
	if hashed {
		h := v.hash
		a.Hash = &h
		if exists && matchesHash(ce, &h, le.Size) {
			a.Kind = ActionKeep
			a.Reason = "内容一致"
			p.plan.Keeps = append(p.plan.Keeps, a)
			p.dependencies[rel] = a
			return
		}
		for _, version := range p.versions[rel] {
			if p.scan.UnsafeNames[version.Path] != "" || !matchesHash(version, &h, le.Size) {
				continue
			}
			a.Kind = ActionRename
			a.SourcePath = version.Path
			a.Reason = "已按完整内容哈希匹配本地版本，重命名为本地名称"
			if exists {
				a.Reason += "；先将同名旧版移入回收站"
			}
			p.plan.Renames = append(p.plan.Renames, a)
			p.sources[version.Path] = true
			p.dependencies[rel] = a
			return
		}
	}
	a.Kind = ActionPending
	a.Reason = "未找到可确认与本地一致的云端版本，等待官方备份；保留此文件的历史版本"
	p.plan.Pending = append(p.plan.Pending, a)
	protect()
}

func (p *cleanupPlanner) excluded(ce RemoteEntry) bool {
	opt := LocalScanOptions{SkipSuffixes: p.opt.SkipSuffixes, SkipNames: p.opt.SkipNames, SkipPrefixes: p.opt.SkipPrefixes}
	for _, name := range strings.Split(RelPath(p.scan.Root, ce.Path), "/") {
		if shouldSkipLocal(name, opt) {
			return true
		}
	}
	return false
}

func (p *cleanupPlanner) classifyExtras() {
	skipped := 0
	protectedDirs := make(map[string]bool)
	for scope := range p.protected {
		for parent := ParentRemote(scope); parent != p.scan.Root && parent != "/"; parent = ParentRemote(parent) {
			protectedDirs[parent] = true
		}
	}
	type counts struct {
		files int
		bytes int64
	}
	totals := make(map[string]counts)
	for abs, ce := range p.scan.Entries {
		protected := p.plan.scopeUnverified(RelPath(p.scan.Root, abs)) || p.protected[abs] || p.sources[abs] || p.excluded(ce) || p.scan.UnsafeNames[abs] != ""
		if _, local := p.local.Entries[RelPath(p.scan.Root, abs)]; local {
			protected = true
		}
		for parent := ParentRemote(abs); parent != p.scan.Root && parent != "/"; parent = ParentRemote(parent) {
			if protected {
				protectedDirs[parent] = true
			}
			if !ce.IsDir {
				t := totals[parent]
				t.files++
				t.bytes += ce.Size
				totals[parent] = t
			}
		}
	}
	removedDirs := make(map[string]bool)
	paths := append([]string(nil), p.scan.Order...)
	sort.Strings(paths)
	for _, abs := range paths {
		ce := p.scan.Entries[abs]
		rel := RelPath(p.scan.Root, abs)
		if p.plan.scopeUnverified(rel) {
			continue
		}
		if _, exists := p.local.Entries[rel]; exists {
			continue
		}
		protected := false
		for ancestor := abs; ancestor != p.scan.Root && ancestor != "/"; ancestor = ParentRemote(ancestor) {
			if p.protected[ancestor] || p.scan.UnsafeNames[ancestor] != "" && ancestor != abs {
				protected = true
				break
			}
		}
		if p.excluded(ce) {
			skipped++
			continue
		}
		if protected || p.sources[abs] {
			continue
		}
		if p.scan.UnsafeNames[abs] != "" {
			p.plan.Conflicts = append(p.plan.Conflicts, PlannedAction{Kind: ActionConflict, RelPath: rel, RemotePath: abs, Reason: "云端名称无法映射，保留此项"})
			continue
		}
		inside := false
		for parent := ParentRemote(abs); parent != p.scan.Root && parent != "/"; parent = ParentRemote(parent) {
			if removedDirs[parent] {
				inside = true
				break
			}
		}
		if inside {
			continue
		}
		if ce.IsDir && protectedDirs[abs] {
			continue
		}
		a := PlannedAction{Kind: ActionRemove, RelPath: rel, RemotePath: abs, Size: ce.Size, IsDir: ce.IsDir, Reason: "本地不存在，移入网盘回收站"}
		if owner := p.owners[abs]; owner != "" {
			dep, ok := p.dependencies[owner]
			if !ok {
				continue
			}
			a.Requires = dep.RemotePath
			a.Hash = dep.Hash
			a.LocalPath = dep.LocalPath
			a.LocalSize = dep.LocalSize
			a.LocalMtime = dep.LocalMtime
			a.LocalMtimeNano = dep.LocalMtimeNano
			a.Reason = "历史副本；确认本地对应版本在云端就位后移入回收站"
		}
		if ce.IsDir {
			removedDirs[abs] = true
			a.Size = totals[abs].bytes
			a.RemovedFiles = totals[abs].files
			a.WholeDir = p.scan.PrunedDirs[abs]
			if a.WholeDir {
				a.Reason = "本地无此目录：连同全部内容一次移入回收站；未扫描内部文件，数量与容量未知"
			}
		}
		p.plan.Removes = append(p.plan.Removes, a)
	}
	if skipped > 0 {
		p.plan.SkipNotices = append(p.plan.SkipNotices, fmt.Sprintf("保留 %d 个符合排除规则的云端条目", skipped))
	}
}

func sortActions(a []PlannedAction) {
	sort.Slice(a, func(i, j int) bool { return a[i].RelPath < a[j].RelPath })
}
