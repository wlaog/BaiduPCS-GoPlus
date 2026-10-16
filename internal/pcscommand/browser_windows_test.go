package pcscommand

import (
	"path/filepath"
	"testing"
)

func TestEdgeUsesSeparateProfilesForGUIAndElevation(t *testing.T) {
	configDir := t.TempDir()
	url := "http://127.0.0.1:1234/?token=test&extra=value"
	var profiles []string
	for _, elevated := range []bool{false, true} {
		cmd := edgeBrowserCommand("msedge.exe", configDir, url, elevated)
		profile := edgeBrowserProfile(configDir, elevated)
		if filepath.Dir(profile) != configDir {
			t.Fatal("browser profile must stay in the project config directory")
		}
		if cmd.Args[1] != "--user-data-dir="+profile || cmd.Args[len(cmd.Args)-1] != "--app="+url {
			t.Fatalf("browser must use a private profile and pass the URL as one argument: %v", cmd.Args)
		}
		profiles = append(profiles, profile)
	}
	if profiles[0] == profiles[1] {
		t.Fatal("elevated and normal GUI instances must not share a browser profile")
	}
}
