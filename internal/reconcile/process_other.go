//go:build !windows

package reconcile

import (
	"os"
	"path/filepath"
	"strings"
)

// detectClientProcessesPlatform reports running official-client processes on
// non-Windows platforms. The desktop client does not exist there, but the probe
// is shared code, so this returns whatever best-effort signal exists.
func detectClientProcessesPlatform() []string {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var found []string
	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", p.Name(), "comm"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		for _, want := range officialClientProcessNames {
			if strings.EqualFold(name, strings.TrimSuffix(want, ".exe")) {
				found = append(found, name)
				break
			}
		}
	}
	return found
}
