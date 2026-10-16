package reconcile

import (
	"path"
	"strings"
)

// ScanIssue names the smallest known scope whose contents are unverified.
// It is kept separate from WAIT: a failed read says nothing about backup state.
type ScanIssue struct {
	Side    string `json:"side"`
	RelPath string `json:"relPath"`
	Reason  string `json:"reason"`
}

func relContains(parent, child string) bool {
	return parent == "" || parent == child || strings.HasPrefix(child, parent+"/")
}
func relOverlaps(a, b string) bool { return relContains(a, b) || relContains(b, a) }
func validIssueRel(rel string) bool {
	return rel != "" && rel != "." && !strings.HasPrefix(rel, "/") && path.Clean(rel) == rel && rel != ".." && !strings.HasPrefix(rel, "../")
}
func scanIssues(local *LocalScanResult, cloud *CloudScan) []ScanIssue {
	issues := make([]ScanIssue, 0, len(local.Errors)+len(cloud.Stats.Errors))
	for _, e := range local.Errors {
		issues = append(issues, ScanIssue{Side: "本地", RelPath: e.Path, Reason: e.Err})
	}
	for _, e := range cloud.Stats.Errors {
		issues = append(issues, ScanIssue{Side: "云端", RelPath: RelPath(cloud.Root, e.Path), Reason: e.Err})
	}
	return issues
}
func (p *Plan) scopeUnverified(rel string) bool {
	for _, issue := range p.ScanIssues {
		if relContains(issue.RelPath, rel) {
			return true
		}
		// Failed stat/read of a canonical local file must also protect its
		// candidate history names, even when no LocalEntry could be recorded.
		if path.Dir(rel) == path.Dir(issue.RelPath) {
			name := path.Base(rel)
			for {
				base, ok := stripVersionSuffix(name)
				if !ok {
					break
				}
				if base == path.Base(issue.RelPath) {
					return true
				}
				name = base
			}
		}
	}
	return false
}
func (p *Plan) actionUnverified(a PlannedAction) bool {
	for _, issue := range p.ScanIssues {
		for _, remote := range []string{a.RemotePath, a.SourcePath, a.Requires} {
			if remote != "" && (relOverlaps(issue.RelPath, RelPath(p.RemoteRoot, remote)) || p.scopeUnverified(RelPath(p.RemoteRoot, remote))) {
				return true
			}
		}
	}
	return false
}
func (p *Plan) CanApply() bool {
	return p != nil && ((p.ScanComplete && p.LocalScanComplete && len(p.ScanErrors) == 0) || p.PartialScanSafe)
}
