package reconcile

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestChinesePathByteLengthDoesNotCreateConflict(t *testing.T) {
	root := t.TempDir()
	rel := strings.Repeat("中", 30) + "/" + strings.Repeat("文", 30) + "/" + strings.Repeat("件", 30) + "/照片(1).JPG"
	localPath := filepath.Join(root, filepath.FromSlash(rel))
	if len(localPath) <= 250 {
		t.Fatal("fixture must exceed the old UTF-8 byte threshold")
	}
	if len(utf16.Encode([]rune(localPath))) >= 250 {
		t.Fatal("fixture should remain a short Windows path")
	}
	writeLocal(t, root, rel+"|image")
	fs := newFakeFS().addTree("/backup", rel+"|5|"+md5hex([]byte("image")))
	plan, err := BuildPlan(fs, PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 0 || len(plan.Keeps) != 1 || !plan.ZeroDifference() {
		t.Fatalf("readable Chinese path must participate in normal hash comparison: conflicts=%v", plan.Conflicts)
	}
	// The same path waits for official backup when missing remotely. Neither the
	// character count nor the local root's byte length is a cloud restriction.
	plan, err = BuildPlan(newFakeFS().addTree("/backup"), PlanOptions{LocalRoot: root, RemoteRoot: "/backup"})
	if err != nil || len(plan.Conflicts) != 0 || !hasRel(plan.Pending, rel) {
		t.Fatalf("missing long UTF-8 path must wait for official backup: %v", err)
	}
}
