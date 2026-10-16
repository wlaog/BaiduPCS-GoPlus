package reconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/checksum"
)

func TestUploadDatabaseUsesConfigAndCleansUp(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv(pcsconfig.EnvConfigDir, configDir)
	// An unusable system temp directory must not break GUI uploads.
	t.Setenv("TEMP", filepath.Join(configDir, "missing"))
	t.Setenv("TMP", filepath.Join(configDir, "missing"))
	t.Setenv("TMPDIR", filepath.Join(configDir, "missing"))
	db, cleanup, err := newEmptyUploadingDatabase()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if db.Search(&checksum.LocalFileMeta{Path: "new.txt"}) != nil {
		t.Fatal("private database must be empty")
	}
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(configDir, "reconcile-uploadstate-*.json"))
	if len(files) != 1 {
		t.Fatal("upload state must be stored under the selected config directory")
	}
	cleanup()
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("temporary upload state was not removed")
	}
}

func TestUploadStopsWhenDatabaseCannotBeCreated(t *testing.T) {
	local := filepath.Join(t.TempDir(), "one-byte.txt")
	if err := os.WriteFile(local, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// A file cannot serve as a config directory. No cloud call may follow.
	t.Setenv(pcsconfig.EnvConfigDir, local)
	w := NewRemoteWriter(baidupcs.NewPCS(0, ""))
	if err := w.Upload(local, "/test/one-byte.txt", 1); err == nil || !strings.Contains(err.Error(), "状态库") {
		t.Fatalf("expected a database initialization error, got %v", err)
	}
}

func TestApplyReportEmptyActionsAreJSONArrays(t *testing.T) {
	data, err := json.Marshal((&ApplyReport{}).ToJSON())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mkdirs", "uploads", "verifies", "removes"} {
		if string(fields[name]) != "[]" {
			t.Fatalf("%s must be an array even when no actions were performed", name)
		}
	}
}
