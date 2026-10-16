package reconcile

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/converter"
)

// UserInfo is the identity the CLI layer supplies for the report header. It
// carries no credential material by design.
type UserInfo struct {
	Name string
	UID  uint64
}

// ProbeOptions configures the read-only feasibility reconnaissance.
//
// The probe performs no mutation of any kind: it cannot, because it is written
// against the Reader interface, which exposes only listing and metadata.
type ProbeOptions struct {
	// RemoteRoot is the cloud directory to inspect, e.g. "/来自：本地电脑/WorkSync".
	RemoteRoot string
	// LocalDir, when set, enables the local-vs-cloud content-hash comparison.
	LocalDir string
	// HashSamples is how many cloud files to hash-check against local files.
	HashSamples int
	// DiscoverRoots additionally lists "/" and reports directories that look
	// like official-client backup roots.
	DiscoverRoots bool
	// Redact removes account-identifying values from the saved report.
	Redact bool
	// ShallowScan limits enumeration to the direct children of RemoteRoot. It
	// keeps a root discovery cheap: walking an entire account to list its
	// top-level folders is slow and can trip the provider's rate limiting.
	ShallowScan bool
	// Limits bounds the enumeration.
	Limits ScanLimits
}

// ProbeMD5Verdict summarises whether the server-reported content MD5 can be
// trusted to confirm equality.
type ProbeMD5Verdict struct {
	// TrustedCompared is the number of files where the server hash was
	// single-block, and therefore trusted.
	TrustedCompared int
	// TrustedMatched is how many of those matched the locally computed hash.
	TrustedMatched int
	// TrustedMismatched is how many disagreed. A non-zero value means the
	// server's field is not a content hash for these files.
	TrustedMismatched int
	// UntrustedCompared counts multi-block files, where the server hash is
	// known-unreliable and therefore only recorded.
	UntrustedCompared int
	// UntrustedMatched is an informational count for those files.
	UntrustedMatched int
	// Notes carries human-readable caveats.
	Notes []string
}

// EqualHashesVerdict reports whether the server's MD5 field can be used as a
// positive equality test.
func (v ProbeMD5Verdict) EqualHashesVerdict() string {
	switch {
	case v.TrustedCompared == 0:
		return "UNKNOWN"
	case v.TrustedMismatched == 0 && v.TrustedMatched > 0:
		return "USABLE_AS_EQUALITY_PROOF"
	case v.TrustedMismatched > 0 && v.TrustedMatched > 0:
		return "PARTIALLY_RELIABLE"
	default:
		return "NOT_A_CONTENT_HASH"
	}
}

// ProbeHashSample is one file whose local and cloud hashes were compared.
type ProbeHashSample struct {
	RelPath           string `json:"relPath"`
	LocalPath         string `json:"localPath"`
	Size              int64  `json:"size"`
	LocalMD5          string `json:"localMd5"`
	LocalSliceMD5     string `json:"localSliceMd5"`
	CloudMD5          string `json:"cloudMd5"`
	CloudMD5Trusted   bool   `json:"cloudMd5Trusted"`
	CloudBlockCount   int    `json:"cloudBlockCount"`
	FullMatch         bool   `json:"fullMatch"`
	SliceMatch        bool   `json:"sliceMatch"`
	ServerLooksLikeSliceMD5 bool `json:"serverLooksLikeSliceMd5"`
	Note              string `json:"note,omitempty"`
}

// RootCandidate is a directory that looks like an official-client backup root.
type RootCandidate struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	IsDir    bool   `json:"isDir"`
	Children int    `json:"children"`
	Matched  string `json:"matched,omitempty"`
}

// LocalOnlyEntry is a local item absent from the cloud (informational).
type LocalOnlyEntry struct {
	RelPath string `json:"relPath"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
}

// ProbeReport is the complete, JSON-serialisable outcome of a probe run.
type ProbeReport struct {
	ProbeVersion   string `json:"probeVersion"`
	GeneratedAt    string `json:"generatedAt"`
	CloudRoot      string `json:"cloudRoot"`
	LocalDir       string `json:"localDir,omitempty"`
	AccountName    string `json:"accountName,omitempty"`
	ServerUser     string `json:"accountUid,omitempty"`
	CloudMetadata  *RemoteEntry `json:"cloudRootMetadata,omitempty"`
	RootCandidates []RootCandidate `json:"rootCandidates,omitempty"`
	Scan           *CloudScan      `json:"scan,omitempty"`
	StructuralDigest string        `json:"structuralDigest,omitempty"`
	MD5            ProbeMD5Verdict `json:"md5Verdict"`
	HashSamples    []ProbeHashSample `json:"hashSamples,omitempty"`
	LocalOnlySample []LocalOnlyEntry `json:"localOnlySample,omitempty"`
	LocalFileCount  int   `json:"localFileCount"`
	LocalDirCount   int   `json:"localDirCount"`
	LocalTotalBytes int64 `json:"localTotalBytes"`
	ClientProcesses []string `json:"officialClientProcesses,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Errors          []string `json:"errors,omitempty"`
	Verdicts        []string `json:"verdicts,omitempty"`
}

// ProbeVersion identifies the probe implementation in saved reports.
const ProbeVersion = "0.1.0"

// backupNameHints are substrings that the official Baidu Netdisk client uses for
// its backup roots. Matching is deliberately loose and only drives *reporting*:
// the operator confirms the exact path, and nothing is ever deleted because a
// directory matched a hint.
var backupNameHints = []string{"来自", "本地电脑", "我的电脑", "备份", "backup", "My Computer", "自动备份"}

// RunProbe performs the reconnaissance and returns a report. It only reads.
func RunProbe(r Reader, user *UserInfo, opt ProbeOptions) (*ProbeReport, error) {
	if r == nil {
		return nil, ErrNoClient
	}
	opt.RemoteRoot = NormalizeRemoteRoot(opt.RemoteRoot)
	if opt.HashSamples <= 0 {
		opt.HashSamples = 10
	}

	report := &ProbeReport{
		ProbeVersion: ProbeVersion,
		GeneratedAt:  time.Now().Format(time.RFC3339),
		CloudRoot:    opt.RemoteRoot,
		LocalDir:     opt.LocalDir,
	}
	if user != nil {
		report.AccountName = user.Name
	}
	if opt.Redact {
		report.Redact()
	}

	if opt.DiscoverRoots {
		candidates, err := DiscoverBackupRoots(r)
		if err != nil {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("枚举云端根目录失败: %v (请手动指定 --remote)", err))
		}
		report.RootCandidates = candidates
	}

	limits := opt.Limits
	if opt.ShallowScan && limits.MaxDepth == 0 {
		// Ask for depth 1 deliberately and keep the safety backstop separate,
		// so an intentional bound is not reported as an enumeration failure.
		limits.ExpectedMaxDepth = 1
		limits.MaxDepth = 1
	}
	scan, err := ScanCloud(r, opt.RemoteRoot, limits)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
		report.Verdicts = append(report.Verdicts, "ABORT: 无法枚举目标云端目录")
		return report, nil
	}
	report.Scan = scan
	report.StructuralDigest = scan.StructuralDigest()

	if meta, metaErr := r.Meta(opt.RemoteRoot); metaErr == nil {
		m := meta
		report.CloudMetadata = &m
	}

	// Integrity verdicts.
	if len(scan.Stats.Errors) > 0 {
		report.Verdicts = append(report.Verdicts,
			fmt.Sprintf("FAIL: 云端目录枚举不完整 (%d 处失败) — 禁止在本次结果上执行任何清理", len(scan.Stats.Errors)))
	} else {
		report.Verdicts = append(report.Verdicts, "PASS: 云端目录可完整枚举")
	}
	if len(scan.Stats.LargeDirs) > 0 {
		report.Verdicts = append(report.Verdicts,
			fmt.Sprintf("WARN: %d 个目录子项数达到分页上限 %d, 上游分页异常时会静默返回部分数据, 需人工复核",
				len(scan.Stats.LargeDirs), PanicPageSize))
	}
	if scan.ShortOfRequestedDepth() {
		report.Verdicts = append(report.Verdicts,
			fmt.Sprintf("FAIL: 枚举在到达请求深度前被截断于 %s", scan.Stats.TruncatedAt))
	}
	if len(scan.Stats.DepthBounded) > 0 {
		report.Verdicts = append(report.Verdicts,
			fmt.Sprintf("INFO: 按请求的深度上限停止于 %d 个目录 (非故障): %s",
				len(scan.Stats.DepthBounded), sampleList(scan.Stats.DepthBounded, 3)))
	}
	if len(scan.UnsafeNames) > 0 {
		report.Verdicts = append(report.Verdicts,
			fmt.Sprintf("WARN: %d 个云端名称无法在 Windows 上忠实映射, 将一律保留不删", len(scan.UnsafeNames)))
	}
	if len(scan.DuplicateLike) > 0 {
		report.Verdicts = append(report.Verdicts,
			fmt.Sprintf("INFO: 发现 %d 个形如 name(n).ext 的疑似官方备份副本 (仅报告, 不据此删除)", len(scan.DuplicateLike)))
	}
	report.ClientProcesses = DetectOfficialClientProcesses()
	if len(report.ClientProcesses) > 0 {
		report.Verdicts = append(report.Verdicts,
			fmt.Sprintf("WARN: 官方百度网盘客户端进程正在运行 (%s) — 执行清理前必须先暂停自动备份并退出客户端",
				strings.Join(report.ClientProcesses, ", ")))
	}

	if opt.LocalDir != "" {
		probeLocalSide(r, report, scan, opt)
	}
	return report, nil
}

// DiscoverBackupRoots lists "/" and reports directories whose names match the
// official client's backup-root naming, plus their immediate child counts.
func DiscoverBackupRoots(r Reader) ([]RootCandidate, error) {
	children, err := r.ListDir("/")
	if err != nil {
		return nil, err
	}
	var out []RootCandidate
	for _, c := range children {
		matched := matchBackupHint(c.Name)
		if matched == "" {
			continue
		}
		cand := RootCandidate{Path: c.Path, Name: c.Name, IsDir: c.IsDir, Matched: matched}
		if c.IsDir {
			if grand, gErr := r.ListDir(c.Path); gErr == nil {
				cand.Children = len(grand)
			}
		}
		out = append(out, cand)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func matchBackupHint(name string) string {
	lower := strings.ToLower(name)
	for _, hint := range backupNameHints {
		if strings.Contains(lower, strings.ToLower(hint)) {
			return hint
		}
	}
	return ""
}

// probeLocalSide compares a local directory against the scanned cloud tree and
// hash-checks a deterministic sample of files.
func probeLocalSide(r Reader, report *ProbeReport, scan *CloudScan, opt ProbeOptions) {
	localEntries, localErr := ScanLocal(opt.LocalDir, LocalScanOptions{})
	if localErr != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("扫描本地目录失败: %v", localErr))
		report.Verdicts = append(report.Verdicts, "ABORT: 本地目录无法扫描")
		return
	}
	report.LocalFileCount = localEntries.Stats.FileCount
	report.LocalDirCount = localEntries.Stats.DirCount
	report.LocalTotalBytes = localEntries.Stats.TotalBytes

	// Cloud paths with no local counterpart, and vice versa. Both are reported;
	// neither causes any action here.
	localSet := make(map[string]LocalEntry, len(localEntries.Entries))
	for rel, e := range localEntries.Entries {
		localSet[rel] = e
	}
	var localOnly []LocalOnlyEntry
	for rel, e := range localEntries.Entries {
		if _, ok := scan.Entries[JoinRemote(scan.Root, rel)]; !ok {
			localOnly = append(localOnly, LocalOnlyEntry{RelPath: rel, Size: e.Size, IsDir: e.IsDir})
		}
	}
	sort.Slice(localOnly, func(i, j int) bool { return localOnly[i].RelPath < localOnly[j].RelPath })
	if len(localOnly) > 50 {
		localOnly = localOnly[:50]
	}
	report.LocalOnlySample = localOnly

	if opt.HashSamples <= 0 || len(scan.Entries) == 0 {
		report.MD5.Notes = append(report.MD5.Notes, "未进行哈希抽样比对")
		return
	}
	sampleFileHashes(report, scan, localEntries, opt.HashSamples)
}

// sampleFileHashes picks an evenly spread, deterministic set of files present on
// both sides and compares locally computed hashes with the server's field.
func sampleFileHashes(report *ProbeReport, scan *CloudScan, local *LocalScanResult, n int) {
	type pair struct {
		rel   string
		local LocalEntry
		cloud RemoteEntry
	}
	var pairs []pair
	for rel, le := range local.Entries {
		if le.IsDir {
			continue
		}
		ce, ok := scan.Entries[JoinRemote(scan.Root, rel)]
		if !ok || ce.IsDir {
			continue
		}
		pairs = append(pairs, pair{rel: rel, local: le, cloud: ce})
	}
	if len(pairs) == 0 {
		report.MD5.Notes = append(report.MD5.Notes, "云端与本地没有同名文件可供哈希比对")
		return
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].rel < pairs[j].rel })

	step := len(pairs) / n
	if step < 1 {
		step = 1
	}
	verdict := &report.MD5
	for i := 0; i < len(pairs) && len(report.HashSamples) < n; i += step {
		p := pairs[i]
		s := ProbeHashSample{
			RelPath:         p.rel,
			LocalPath:       p.local.AbsPath,
			Size:            p.local.Size,
			CloudMD5:        p.cloud.ContentMD5,
			CloudMD5Trusted: p.cloud.ContentMD5Trusted,
			CloudBlockCount: p.cloud.BlockCount,
		}
		// LocalContentHash is used rather than upstream checksum.GetFileSum
		// because the latter returns nil hashes for a zero-byte file: its
		// ChecksumWriteUnit.write returns early when End <= 0 without ever
		// calling Sum(). Computing the hash here keeps empty files comparable.
		full, err := LocalContentHash(p.local.AbsPath)
		if err != nil {
			s.Note = fmt.Sprintf("本地读取失败: %v", err)
			report.HashSamples = append(report.HashSamples, s)
			continue
		}
		s.LocalMD5 = full.MD5
		s.LocalSliceMD5 = full.SliceMD5
		cloud := strings.ToLower(strings.TrimSpace(p.cloud.ContentMD5))
		s.FullMatch = cloud != "" && cloud == s.LocalMD5
		s.SliceMatch = cloud != "" && cloud == s.LocalSliceMD5
		// The server is expected to expose the full content hash; if a value
		// ever matches only the leading-256KB digest, that is worth surfacing.
		s.ServerLooksLikeSliceMD5 = s.SliceMatch && !s.FullMatch

		if p.cloud.ContentMD5Trusted {
			verdict.TrustedCompared++
			if s.FullMatch {
				verdict.TrustedMatched++
			} else {
				verdict.TrustedMismatched++
			}
		} else {
			verdict.UntrustedCompared++
			if s.FullMatch {
				verdict.UntrustedMatched++
			}
		}
		report.HashSamples = append(report.HashSamples, s)
	}
	verdict.Notes = append(verdict.Notes,
		"「字段可用」表示服务端返回了 32 位十六进制的 md5, 可用于比对。",
		"实测: /api/list 返回的是全文件内容 MD5, 多分片大文件也准确; 它不返回 block_list。",
		"结论只用于「能否用哈希证明两个文件相同」; 哈希不一致永远不作为内容不同的唯一证据。")
	report.Verdicts = append(report.Verdicts, fmt.Sprintf("MD5 可用性: %s (可用样本 %d, 全量一致 %d, 不一致 %d)",
		verdict.EqualHashesVerdict(), verdict.TrustedCompared, verdict.TrustedMatched, verdict.TrustedMismatched))
}

// officialClientProcessNames are the Windows process names of the official
// Baidu Netdisk client and its helpers.
//
// BaiduNetdiskUnite.exe is the current packaged client; the older single-process
// names are kept because both are still in the wild. Getting this list wrong is
// how a running client goes unnoticed, so it errs towards including names.
var officialClientProcessNames = []string{
	"BaiduNetdisk.exe",
	"BaiduNetdiskUnite.exe",
	"baidunetdiskhost.exe",
	"BaiduNetdiskHost.exe",
	"YunGuanJia.exe",
}

// DetectOfficialClientProcesses reports running official-client processes.
//
// This is a best-effort advisory signal for the operator: it cannot prove a
// backup task is active (the client may be running but paused), and its absence
// cannot prove the task is stopped. The authoritative answer is the client's own
// backup settings, which the operator must check.
//
// It is also known to be unreliable inside a restricted sandbox, where process
// enumeration may be denied or filtered. A negative result therefore means
// "no signal", never "not running".
func DetectOfficialClientProcesses() []string {
	return detectClientProcessesPlatform()
}

// ProbeJSON serialises a report.
func (r *ProbeReport) ProbeJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Redact removes account-identifying values from the report so that it can be
// shared for review without exposing who the account belongs to.
//
// It deliberately does NOT touch file paths: the operator needs to see those to
// confirm the mapping is correct, and paths are the substance of the report.
// Credential material is never put into a report in the first place, so there is
// nothing of that kind to strip here.
func (r *ProbeReport) Redact() {
	if r.AccountName != "" {
		r.AccountName = "<已隐去账号名>"
	}
	r.ServerUser = ""
}

// WriteProbeReport writes the JSON report and returns its path.
func WriteProbeReport(dir string, r *ProbeReport) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("probe-%s.json", time.Now().Format("20060102-150405"))
	full := filepath.Join(dir, name)
	data, err := r.ProbeJSON()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return full, nil
}

// RenderProbeHuman writes a compact, human-readable summary. It intentionally
// prints no credential material and no cloud MD5 values beyond the explicitly
// requested hash samples.
func RenderProbeHuman(w io.Writer, r *ProbeReport) {
	p := func(format string, a ...interface{}) { fmt.Fprintf(w, format+"\n", a...) }
	p("=== BaiduPCS-GoPlus 对齐本地 · 可行性探测报告 ===")
	p("探测版本: %s   生成时间: %s", r.ProbeVersion, r.GeneratedAt)
	if r.AccountName != "" {
		p("当前账号: %s", r.AccountName)
	}
	p("云端根目录: %s", r.CloudRoot)
	if r.LocalDir != "" {
		p("本地目录:   %s", r.LocalDir)
	}
	p("")

	if len(r.RootCandidates) > 0 {
		p("--- 疑似官方备份根目录 (来自云端 /) ---")
		for _, c := range r.RootCandidates {
			p("  %s  [命中: %s] 子项=%d", c.Path, c.Matched, c.Children)
		}
		p("")
	}

	if r.CloudMetadata != nil {
		p("--- 目标目录元信息 ---")
		p("  路径: %s", r.CloudMetadata.Path)
		p("  类型: %s", typeLabel(r.CloudMetadata.IsDir))
		p("  fs_id: %d", r.CloudMetadata.FsID)
		if !r.CloudMetadata.IsDir {
			p("  大小: %s", converter.ConvertFileSize(r.CloudMetadata.Size, 2))
		}
		p("")
	}

	if s := r.Scan; s != nil {
		p("--- 云端树枚举 ---")
		p("  目录数: %d   文件数: %d   总大小: %s", s.Stats.DirCount, s.Stats.FileCount,
			converter.ConvertFileSize(s.Stats.TotalBytes, 2))
		p("  最大深度: %d   列举请求数: %d", s.Stats.MaxDepth, s.Stats.ListingRequests)
		p("  结构指纹: %s", r.StructuralDigest)
		if s.ShortOfRequestedDepth() {
			p("  !! 枚举被截断于: %s", s.Stats.TruncatedAt)
		} else if len(s.Stats.DepthBounded) > 0 {
			p("  按请求的深度上限停止于 %d 个目录 (上限内的结果完整)", len(s.Stats.DepthBounded))
		}
		for _, e := range s.Stats.Errors {
			p("  !! 枚举失败: %s -> %s", e.Path, e.Err)
		}
		for dir, count := range s.Stats.LargeDirs {
			p("  ! 大目录(达到分页上限 %d): %s -> %d 项", PanicPageSize, dir, count)
		}
		if len(s.UnsafeNames) > 0 {
			p("  无法在 Windows 忠实映射的名称: %d", len(s.UnsafeNames))
			shown := 0
			for path, reason := range s.UnsafeNames {
				if shown >= 10 {
					p("    ... 其余省略")
					break
				}
				p("    %s -> %s", path, reason)
				shown++
			}
		}
		if len(s.DuplicateLike) > 0 {
			p("  疑似官方副本 name(n).ext: %d 个 (仅报告, 不据此删除)", len(s.DuplicateLike))
			for i, path := range s.DuplicateLike {
				if i >= 10 {
					p("    ... 其余省略")
					break
				}
				p("    %s", path)
			}
		}
		p("")
	}

	if r.LocalDir != "" {
		p("--- 本地树 ---")
		p("  目录数: %d   文件数: %d   总大小: %s", r.LocalDirCount, r.LocalFileCount,
			converter.ConvertFileSize(r.LocalTotalBytes, 2))
		if len(r.LocalOnlySample) > 0 {
			p("  本地有、云端缺失(前 50 项, 即未来 UPLOAD 候选): %d 项", len(r.LocalOnlySample))
			for i, e := range r.LocalOnlySample {
				if i >= 10 {
					p("    ... 其余省略")
					break
				}
				p("    %s (%s)", e.RelPath, converter.ConvertFileSize(e.Size, 2))
			}
		}
		p("")
	}

	if len(r.HashSamples) > 0 {
		p("--- 内容哈希抽样比对 (服务端字段 vs 本地计算) ---")
		p("  %-40s %-10s %-8s %s", "相对路径", "字段可用", "全量一致", "说明")
		for _, s := range r.HashSamples {
			note := s.Note
			if note == "" && s.ServerLooksLikeSliceMD5 {
				note = "服务端值等于前 256KB 切片 md5"
			}
			if note == "" && s.CloudMD5 == "" {
				note = "服务端未返回 md5"
			}
			p("  %-40s %-10v %-8v %s", truncate(s.RelPath, 40), s.CloudMD5Trusted, s.FullMatch, note)
		}
		p("  可用样本: %d (全量一致 %d / 不一致 %d); 不可用样本: %d",
			r.MD5.TrustedCompared, r.MD5.TrustedMatched, r.MD5.TrustedMismatched,
			r.MD5.UntrustedCompared)
		p("  结论: %s", r.MD5.EqualHashesVerdict())
		for _, n := range r.MD5.Notes {
			p("    - %s", n)
		}
		p("")
	}

	if len(r.ClientProcesses) > 0 {
		p("--- 官方客户端进程 ---")
		for _, name := range r.ClientProcesses {
			p("  正在运行: %s", name)
		}
		p("")
	}

	if len(r.Verdicts) > 0 {
		p("--- 判定 ---")
		for _, v := range r.Verdicts {
			p("  %s", v)
		}
		p("")
	}
	if len(r.Warnings) > 0 {
		p("--- 警告 ---")
		for _, v := range r.Warnings {
			p("  %s", v)
		}
		p("")
	}
	if len(r.Errors) > 0 {
		p("--- 错误 ---")
		for _, v := range r.Errors {
			p("  %s", v)
		}
	}
}

func typeLabel(isDir bool) string {
	if isDir {
		return "目录"
	}
	return "文件"
}

// sampleList renders at most n items for a compact verdict line.
func sampleList(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" …(共 %d)", len(items))
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
