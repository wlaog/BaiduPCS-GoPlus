//go:build windows

package reconcile

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

// tasklistTimeout bounds the external process query so a hung system does not
// stall a reconcile run.
const tasklistTimeout = 5 * time.Second

// detectClientProcessesPlatform reports running official-client processes.
//
// It parses `tasklist` output rather than using the process API, because a
// sandboxed or cross-session token can silently omit processes from an API
// enumeration, which would make a running client look absent. Detection failure
// still returns nothing, and the caller never treats that as proof of absence.
func detectClientProcessesPlatform() []string {
	cmd := exec.Command("tasklist", "/FO", "CSV", "/NH")
	out, err := runWithTimeout(cmd, tasklistTimeout)
	var found []string
	seen := make(map[string]bool)
	if err == nil {
		text := string(out)
		for _, name := range officialClientProcessNames {
			if seen[name] {
				continue
			}
			// CSV rows look like: "BaiduNetdiskUnite.exe","1234","Console","1","123,456 K"
			if containsFold(text, `"`+name+`"`) {
				seen[name] = true
				found = append(found, name)
			}
		}
	}
	// Second, cheap signal: the client's named pipe. Its presence means the
	// client is running even when tasklist is unavailable (for example inside a
	// restricted sandbox that denies process enumeration).
	if len(found) == 0 {
		for _, pipe := range clientPipeNames {
			if _, statErr := os.Stat(pipe); statErr == nil {
				found = append(found, "命名管道 "+pipe+" 存在")
			}
		}
	}
	return found
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// clientPipeNames are the named pipes the official Windows client listens on.
// Their existence is a strong hint that the client is running.
var clientPipeNames = []string{
	`\\.\pipe\BaiduNetdisk`,
	`\\.\pipe\baidunetdiskhost`,
	`\\.\pipe\BaiduNetdiskUnite`,
}

func runWithTimeout(cmd *exec.Cmd, d time.Duration) ([]byte, error) {
	type result struct {
		out []byte
		err error
	}
	ch := make(chan result, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		out, err := cmd.Output()
		ch <- result{out: out, err: err}
	}()
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(d):
		_ = cmd.Process.Kill()
		return nil, exec.ErrNotFound
	}
}
