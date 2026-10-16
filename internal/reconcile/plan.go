package reconcile

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ActionKind is the classification of one path in the reconcile plan.
type ActionKind string

const (
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
	RuleHashMatch ComparisonRule = "哈希一致(服务端单分片, 可信)"
	// RuleHashMismatch: a trustworthy server hash differs.
	RuleHashMismatch ComparisonRule = "哈希不一致(服务端单分片, 可信)"
	// RuleHashUnavailable: the server hash is multi-block or absent, so it
	// cannot confirm equality.
	RuleHashUnavailable ComparisonRule = "服务端哈希不可信(多分片/缺失)"
	// RuleSizeDiffers: sizes differ, which alone proves the content differs.
	RuleSizeDiffers ComparisonRule = "大小不同"
	// RuleSizeOnlyEqual: sizes match but no dependable hash was available.
	RuleSizeOnlyEqual ComparisonRule = "仅大小相同(无法证明内容一致)"
	// RuleMtimeNewer: sizes match and the local copy is newer.
	RuleMtimeNewer ComparisonRule = "大小相同且本地较新"
)

// PlannedAction is one decided operation. Nothing in this file executes it.
type PlannedAction struct {
	Kind ActionKind
	// RelPath is the "/"-separated path relative to both roots ("" for the root
	// itself).
	RelPath string
	// RemotePath is the absolute cloud path this action concerns.
	RemotePath string
	// LocalPath is the absolute local path (empty for REMOVE).
	LocalPath string
	// LocalSize and LocalMtime are the local file's size and mtime as observed
	// during the scan. They are the snapshot the executor re-checks before
	// uploading: comparing against a freshly stat'ed value would make the check
	// vacuous, because the file would always match itself.
	LocalSize  int64
	LocalMtime int64
	// Size is the byte count of the content involved.
	Size int64
	// IsDir reports that the path is a directory.
	IsDir bool
	// Rule records the comparison outcome for file pairs.
	Rule ComparisonRule
	// Reason is the human-readable explanation shown in the preview.
	Reason string
	// Blocked marks an action the executor must not perform because a
	// precondition failed (for example the local file changed since the scan).
	Blocked bool
	// Stable records whether the local file was quiet long enough to be
	// considered settled. An unstable action is still planned, but the executor
	// MUST re-verify the local snapshot before uploading it.
	Stable bool
}

// PlanOptions configures plan generation.
type PlanOptions struct {
	// LocalRoot is the authoritative local directory.
	LocalRoot string
	// RemoteRoot is the cloud directory to make match LocalRoot.
	RemoteRoot string
	// HashFiles computes content hashes for candidate files. It costs local
	// reads and, for large files, cannot make the server side trustworthy, but
	// it upgrades small-file comparisons from "size only" to a proof.
	HashFiles bool
	// Conservative decides what "same size, no trustworthy hash" means.
	// false (the default) re-uploads, which is idempotent and repairs silent
	// divergence. true reports CONFLICT instead, which uploads nothing but
	// leaves potential divergence in place.
	//
	// The polarity is deliberate: this is a plain bool, so the zero value has to
	// be the behaviour a caller who did not think about it should get.
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
	// SizeOnlyEqualReplace defaults via an explicit setter so that a zero-value
	// PlanOptions from a test still behaves like the CLI default.
	return o
}

// Plan is the complete, reviewable difference between the two trees.
type Plan struct {
	GeneratedAt string
	LocalRoot   string
	RemoteRoot  string
	// StructuralDigest identifies the exact cloud snapshot this plan was built
	// from. Re-running a plan and getting the same digest means the cloud state
	// did not move.
	StructuralDigest string
	// UploadDirCount is how many cloud directories must be created first.
	UploadDirCount int

	Uploads   []PlannedAction
	Replaces  []PlannedAction
	Mkdirs    []PlannedAction
	Removes   []PlannedAction
	Keeps     []PlannedAction
	Conflicts []PlannedAction

	// LocalFiles, LocalDirs, CloudFiles, CloudDirs describe the inputs.
	LocalFiles int
	LocalDirs  int
	CloudFiles int
	CloudDirs  int

	LocalBytes int64
	CloudBytes int64

	// ScanComplete is false when the cloud enumeration had gaps. A plan from an
	// incomplete scan must never be applied.
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

// Changes reports how many actions would modify the cloud.
func (p *Plan) Changes() int {
	return len(p.Uploads) + len(p.Replaces) + len(p.Removes) + len(p.Mkdirs)
}

// ZeroDifference reports whether the cloud already matches the local tree.
func (p *Plan) ZeroDifference() bool {
	return p.Changes() == 0
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
		if !a.IsDir {
			n += a.Size
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
		}
	}
	return n
}

// BuildPlan reads both sides and produces a difference plan. It performs no
// mutation: the cloud side is reached only through Reader, and the local side
// only through read-only filesystem calls.
func BuildPlan(r Reader, opt PlanOptions) (*Plan, error) {
	if r == nil {
		return nil, ErrNoClient
	}
	if strings.TrimSpace(opt.LocalRoot) == "" {
		return nil, fmt.Errorf("%w: 必须指定本地目录", ErrUsage)
	}
	remoteRoot := NormalizeRemoteRoot(opt.RemoteRoot)
	if remoteRoot == "/" {
		return nil, fmt.Errorf("%w: 云端目录不能是根目录 /, 请指定具体的备份子目录", ErrUsage)
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	// A negative window disables the mid-write check, which tests need in order
	// to exercise comparison rules on files just written to a temp dir.
	if opt.EditingWindow == 0 {
		opt.EditingWindow = 2 * time.Minute
	}

	plan := &Plan{
		GeneratedAt: opt.Now.Format(time.RFC3339),
		LocalRoot:   opt.LocalRoot,
		RemoteRoot:  remoteRoot,
	}

	// Local side first: if the local tree cannot be read, there is nothing to
	// mirror and no reason to touch the cloud.
	local, err := ScanLocal(opt.LocalRoot, LocalScanOptions{
		SkipSuffixes: opt.SkipSuffixes,
		SkipNames:    opt.SkipNames,
		SkipPrefixes: opt.SkipPrefixes,
	})
	if err != nil {
		return nil, err
	}
	plan.LocalRoot = local.Root
	plan.LocalFiles = local.Stats.FileCount
	plan.LocalDirs = local.Stats.DirCount
	plan.LocalBytes = local.Stats.TotalBytes
	plan.LocalScanComplete = !local.Stats.Truncated
	plan.Warnings = append(plan.Warnings, local.Warnings...)
	if local.Stats.Skipped > 0 {
		plan.SkipNotices = append(plan.SkipNotices, fmt.Sprintf(
			"本地有 %d 个条目被排除规则跳过 (临时文件/符号链接等); 它们在云端的同路径项不会被删除, 需要你单独处理",
			local.Stats.Skipped))
	}

	// Cloud side.
	scan, err := ScanCloud(r, remoteRoot, opt.Limits)
	if err != nil {
		return nil, err
	}
	plan.CloudFiles = scan.Stats.FileCount
	plan.CloudDirs = scan.Stats.DirCount
	plan.CloudBytes = scan.Stats.TotalBytes
	plan.StructuralDigest = scan.StructuralDigest()
	plan.ScanComplete = scan.ScanComplete() && !scan.ShortOfRequestedDepth()
	for _, e := range scan.Stats.Errors {
		plan.ScanErrors = append(plan.ScanErrors, fmt.Sprintf("%s: %s", e.Path, e.Err))
	}
	if len(scan.Stats.LargeDirs) > 0 {
		for dir, n := range scan.Stats.LargeDirs {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"目录 %s 的子项数达到分页上限(%d), 上游分页异常时会静默返回部分数据, 建议人工复核", dir, n))
		}
	}

	planner := &planner{
		plan:    plan,
		opt:     opt,
		local:   local,
		scan:    scan,
		now:     opt.Now,
		dirsSet: make(map[string]bool),
	}
	planner.build()
	return plan, nil
}

type planner struct {
	plan  *Plan
	opt   PlanOptions
	local *LocalScanResult
	scan  *CloudScan
	now   time.Time
	// dirsSet tracks cloud directories already planned for creation so Mkdir
	// actions are not duplicated.
	dirsSet map[string]bool
}

func (p *planner) build() {
	p.classifyLocal()
	p.classifyCloudOnly()
	p.detectCloudAliases()
	p.finalize()
}

// classifyLocal walks the local tree and decides UPLOAD/REPLACE/KEEP/CONFLICT
// for each entry.
func (p *planner) classifyLocal() {
	rels := make([]string, 0, len(p.local.Entries))
	for rel := range p.local.Entries {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		le := p.local.Entries[rel]
		remotePath := JoinRemote(p.scan.Root, rel)
		ce, onCloud := p.scan.Entries[remotePath]

		if le.IsDir {
			if onCloud && !ce.IsDir {
				p.conflict(rel, remotePath, le.AbsPath,
					"本地是目录, 云端同路径是文件: 需要先移走云端文件才能建目录, 请人工确认")
				continue
			}
			if !onCloud {
				p.planMkdir(rel, remotePath)
			}
			// An existing cloud directory needs no action of its own; its
			// children are handled individually.
			continue
		}

		// Local is a regular file.
		if le.Sparse != "" {
			p.conflict(rel, remotePath, le.AbsPath, "本地文件名无法忠实映射到云端: "+le.Sparse)
			continue
		}
		if !onCloud {
			p.upload(rel, remotePath, le)
			continue
		}
		if ce.IsDir {
			p.conflict(rel, remotePath, le.AbsPath,
				"本地是文件, 云端同路径是目录: 不会自动删除云端目录, 请人工确认")
			continue
		}

		// Same path, both regular files: compare.
		action, keep := p.compare(rel, remotePath, le, ce)
		if keep {
			p.plan.Keeps = append(p.plan.Keeps, action)
			continue
		}
		if action.Kind == ActionConflict {
			p.plan.Conflicts = append(p.plan.Conflicts, action)
			continue
		}
		// A replace needs its parent directory present, which it already is.
		p.plan.Replaces = append(p.plan.Replaces, action)
	}
}

// compare decides whether a local/cloud file pair is identical.
//
// The asymmetry is deliberate: a matching trustworthy hash is evidence of
// equality, but a non-matching (or absent) hash is NEVER treated as proof of
// difference unless the sizes actually differ.
func (p *planner) compare(rel, remotePath string, le LocalEntry, ce RemoteEntry) (PlannedAction, bool) {
	base := PlannedAction{
		RelPath:    rel,
		RemotePath: remotePath,
		LocalPath:  le.AbsPath,
		LocalSize:  le.Size,
		LocalMtime: le.Mtime,
		Size:       le.Size,
		IsDir:      false,
	}

	// A file modified very recently is worth flagging, but not worth refusing:
	// in a manually triggered tool the most likely reason a file was touched in
	// the last couple of minutes is that the user just finished editing it and
	// came here to back it up. The real protection against a half-written file
	// is CheckLocalUnchanged, which re-compares the size and mtime snapshot at
	// execution time and skips anything that moved.
	base.Stable = !IsEditing(le, p.now, p.opt.EditingWindow)
	unstabilityNote := ""
	if !base.Stable {
		unstabilityNote = fmt.Sprintf("; 注意: 本地文件在最近 %s 内被修改过, 执行前会重新校验大小与修改时间",
			p.opt.EditingWindow)
	}
	// finish attaches the instability note to whatever reason the rule produces.
	finish := func(a PlannedAction) PlannedAction {
		if unstabilityNote != "" {
			a.Reason += unstabilityNote
		}
		a.Stable = base.Stable
		return a
	}

	// Sizes differ: the content provably differs, no hash needed.
	if le.Size != ce.Size {
		base.Kind = ActionReplace
		base.Rule = RuleSizeDiffers
		base.Reason = fmt.Sprintf("大小不同 (本地 %d / 云端 %d)", le.Size, ce.Size)
		return finish(base), false
	}

	// Sizes agree. Only a trustworthy server hash can prove equality.
	if ce.ContentMD5Trusted && ce.ContentMD5 != "" {
		localHash, err := LocalContentHash(le.AbsPath)
		if err != nil {
			base.Kind = ActionConflict
			base.Reason = "无法读取本地文件以校验哈希: " + err.Error()
			return base, false
		}
		// Re-check that the file did not change while hashing it, so a file
		// being written is not mistaken for a stable one.
		if localHash.Size != le.Size {
			base.Kind = ActionConflict
			base.Reason = "本地文件在校验期间大小发生变化, 可能正在写入"
			return base, false
		}
		if strings.EqualFold(localHash.MD5, ce.ContentMD5) {
			base.Kind = ActionKeep
			base.Rule = RuleHashMatch
			base.Reason = "内容哈希一致"
			return finish(base), true
		}
		base.Kind = ActionReplace
		base.Rule = RuleHashMismatch
		base.Reason = "服务端可信哈希与本地内容不一致 (大小相同但内容不同)"
		return finish(base), false
	}

	// No trustworthy hash: sizes matching is the only agreement, and it does
	// not prove the contents match.
	base.Rule = RuleSizeOnlyEqual
	if p.opt.Conservative {
		base.Kind = ActionConflict
		base.Reason = "大小相同但服务端哈希不可信, 无法证明内容一致 (按 --conservative 保留待确认)"
		return base, false
	}
	base.Kind = ActionReplace
	base.Reason = "大小相同但服务端哈希不可信, 无法排除内容不同, 保守起见重新上传覆盖"
	return finish(base), false
}

// classifyCloudOnly decides REMOVE for cloud entries with no local counterpart.
//
// The important subtlety: a cloud directory with no local directory of the same
// path is removed as a whole, so its children must not also be planned. More
// importantly, an entry named like an excluded local file (for example a .tmp
// that the local scan deliberately skipped) must NOT be removed, because its
// local counterpart may well exist and simply be excluded from the scan.
func (p *planner) classifyCloudOnly() {
	localRels := p.local.Entries

	// Ancestors that exist locally: anything beneath them must be decided by
	// recursing, not by a wholesale directory removal.
	paths := make([]string, 0, len(p.scan.Entries))
	for abs := range p.scan.Entries {
		paths = append(paths, abs)
	}
	sort.Strings(paths)

	// Collect cloud directories that will be removed wholesale so children can
	// be skipped.
	removedDirs := make(map[string]bool)
	for _, abs := range paths {
		ce := p.scan.Entries[abs]
		if !ce.IsDir {
			continue
		}
		rel := RelPath(p.scan.Root, abs)
		if rel == "" {
			continue
		}
		if _, ok := localRels[rel]; ok {
			continue
		}
		// A cloud-only directory is removed as one unit.
		removedDirs[abs] = true
	}

	for _, abs := range paths {
		ce := p.scan.Entries[abs]
		rel := RelPath(p.scan.Root, abs)
		if rel == "" {
			continue
		}
		if _, ok := localRels[rel]; ok {
			continue // handled by classifyLocal
		}
		// A directory whose listing FAILED is not "empty and redundant": we
		// never saw inside it. Removing it would move unread content into
		// quarantine, so it is left alone and reported instead.
		if p.isUnreadDir(abs, ce) {
			p.plan.Conflicts = append(p.plan.Conflicts, PlannedAction{
				Kind: ActionConflict, RelPath: rel, RemotePath: abs, IsDir: ce.IsDir,
				Reason: "该目录枚举失败, 内容未经确认, 不会自动清理",
			})
			continue
		}
		// Anything beneath a directory that is itself being removed has been
		// accounted for by that removal. This must be checked BEFORE the
		// directory branch, or a nested cloud-only directory would also be
		// listed as its own redundant removal.
		if p.insideRemovedDir(abs, removedDirs) {
			continue
		}
		// Skipped by an exclusion rule locally? Then its local counterpart may
		// exist but is excluded, so leave the cloud copy alone.
		if p.excludedLocally(ce) {
			p.plan.SkipNotices = append(p.plan.SkipNotices, fmt.Sprintf(
				"云端 %s 与本地排除规则匹配, 已保留不删 (如确需清理请显式指定)", abs))
			continue
		}
		if ce.IsDir {
			if !removedDirs[abs] {
				continue
			}
			p.plan.Removes = append(p.plan.Removes, PlannedAction{
				Kind: ActionRemove, RelPath: rel, RemotePath: abs, IsDir: true,
				Reason: "云端存在同名目录, 本地没有 (整个目录移入隔离区)",
			})
			continue
		}
		if _, unsafe := p.scan.UnsafeNames[abs]; unsafe {
			p.plan.Conflicts = append(p.plan.Conflicts, PlannedAction{
				Kind: ActionConflict, RelPath: rel, RemotePath: abs, Size: ce.Size,
				Reason: "云端名称无法在 Windows 忠实映射, 本地不可能有对应项, 但不会按名自动删除",
			})
			continue
		}
		p.plan.Removes = append(p.plan.Removes, PlannedAction{
			Kind: ActionRemove, RelPath: rel, RemotePath: abs, Size: ce.Size,
			Reason: "云端存在, 本地没有",
		})
	}

	sortActions(p.plan.Removes)
}

// isUnreadDir reports whether a cloud entry is a directory whose own listing
// failed, or whose subtree contained a failed listing.
//
// Such a directory must never be planned for removal: its contents were never
// enumerated, so "not present locally" cannot be established for them.
func (p *planner) isUnreadDir(abs string, ce RemoteEntry) bool {
	if len(p.scan.Stats.Errors) == 0 {
		return false
	}
	for _, e := range p.scan.Stats.Errors {
		if e.Path == abs {
			return true
		}
		// A failed descendant also makes this directory untrustworthy, but only
		// if the entry is directory-like; a file has no descendants.
		if ce.IsDir && strings.HasPrefix(e.Path, abs+"/") {
			return true
		}
	}
	return false
}

// excludedLocally reports whether a cloud name matches the local exclusion
// rules, meaning the local scan would have skipped its counterpart.
func (p *planner) excludedLocally(ce RemoteEntry) bool {
	opt := LocalScanOptions{
		SkipSuffixes: p.opt.SkipSuffixes,
		SkipNames:    p.opt.SkipNames,
		SkipPrefixes: p.opt.SkipPrefixes,
	}
	return shouldSkipLocal(ce.Name, opt)
}

func (p *planner) insideRemovedDir(abs string, removedDirs map[string]bool) bool {
	for dir := range removedDirs {
		if strings.HasPrefix(abs, dir+"/") {
			return true
		}
	}
	return false
}

// detectCloudAliases looks for the pathological case where the cloud reports
// two entries with the same normalised path. The scanner keys by path, so one
// would silently overwrite the other.
func (p *planner) detectCloudAliases() {
	seen := make(map[string]string, len(p.scan.Order))
	dupes := map[string]int{}
	for _, abs := range p.scan.Order {
		if prev, ok := seen[abs]; ok {
			_ = prev
			dupes[abs]++
			continue
		}
		seen[abs] = abs
	}
	for abs, n := range dupes {
		p.plan.Conflicts = append(p.plan.Conflicts, PlannedAction{
			Kind: ActionConflict, RelPath: RelPath(p.scan.Root, abs), RemotePath: abs,
			Reason: fmt.Sprintf("云端返回了 %d 个规范化后相同的路径, 映射存在歧义, 本次全部不处理", n+1),
		})
	}
}

func (p *planner) finalize() {
	sortActions(p.plan.Uploads)
	sortActions(p.plan.Replaces)
	sortActions(p.plan.Mkdirs)
	sortActions(p.plan.Keeps)
	sortActions(p.plan.Conflicts)

	// A local file that has no cloud counterpart needs every missing ancestor
	// directory created first.
	if len(p.plan.Uploads) > 0 || len(p.plan.Replaces) > 0 {
		// Only uploads can require new directories; a replace implies the path
		// already exists.
		need := map[string]bool{}
		for _, a := range p.plan.Uploads {
			for _, dir := range missingAncestors(p, a.RelPath) {
				need[dir] = true
			}
		}
		dirs := make([]string, 0, len(need))
		for dir := range need {
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			if p.dirsSet[dir] {
				continue
			}
			p.dirsSet[dir] = true
			p.plan.Mkdirs = append(p.plan.Mkdirs, PlannedAction{
				Kind: ActionMkdir, RelPath: dir, RemotePath: JoinRemote(p.scan.Root, dir),
				IsDir: true, Reason: "上传前创建缺失的云端目录",
			})
		}
		sortActions(p.plan.Mkdirs)
	}

	p.plan.UploadDirCount = len(p.plan.Mkdirs)
}

// missingAncestors returns the relative directories on the path to rel that do
// not exist on the cloud yet, outermost first.
func missingAncestors(p *planner, rel string) []string {
	var out []string
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	if len(parts) <= 1 {
		return nil
	}
	cur := ""
	for _, part := range parts[:len(parts)-1] {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		abs := JoinRemote(p.scan.Root, cur)
		entry, ok := p.scan.Entries[abs]
		if ok && entry.IsDir {
			continue
		}
		out = append(out, cur)
	}
	return out
}

// planMkdir records that a local directory is absent on the cloud. The Mkdir
// list is rebuilt in finalize from the uploads' missing ancestors, so this only
// needs to be recorded when it is a leaf directory (an empty local directory
// still needs creating to be mirrored).
func (p *planner) planMkdir(rel, remotePath string) {
	if rel == "" {
		return
	}
	if p.dirsSet[rel] {
		return
	}
	p.dirsSet[rel] = true
	p.plan.Mkdirs = append(p.plan.Mkdirs, PlannedAction{
		Kind: ActionMkdir, RelPath: rel, RemotePath: remotePath, IsDir: true,
		Reason: "本地存在、云端缺失的目录 (含空目录)",
	})
}

func (p *planner) upload(rel, remotePath string, le LocalEntry) {
	stable := !IsEditing(le, p.now, p.opt.EditingWindow)
	reason := "云端缺失"
	if !stable {
		reason = "云端缺失; 注意: 本地文件刚被修改过, 执行前会重新校验"
	}
	p.plan.Uploads = append(p.plan.Uploads, PlannedAction{
		Kind: ActionUpload, RelPath: rel, RemotePath: remotePath, LocalPath: le.AbsPath,
		LocalSize: le.Size, LocalMtime: le.Mtime,
		Size: le.Size, Reason: reason, Stable: stable,
	})
}

func (p *planner) conflict(rel, remotePath, localPath, reason string) {
	p.plan.Conflicts = append(p.plan.Conflicts, PlannedAction{
		Kind: ActionConflict, RelPath: rel, RemotePath: remotePath, LocalPath: localPath,
		Reason: reason,
	})
}

func sortActions(actions []PlannedAction) {
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].RelPath != actions[j].RelPath {
			return actions[i].RelPath < actions[j].RelPath
		}
		return actions[i].RemotePath < actions[j].RemotePath
	})
}
