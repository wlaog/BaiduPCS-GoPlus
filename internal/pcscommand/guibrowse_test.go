package pcscommand

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile"
)

func TestLocalFolderBrowserListsOnlyDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"中文 (资料)", "Alpha"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	listing, err := listLocalFolders(root)
	if err != nil {
		t.Fatal(err)
	}
	if listing.Path != root || !listing.Selectable || listing.Parent != filepath.Dir(root) || len(listing.Folders) != 2 {
		t.Fatalf("unexpected folder listing: %+v", listing)
	}
	if listing.Folders[0].Name != "Alpha" || listing.Folders[1].Path != filepath.Join(root, "中文 (资料)") {
		t.Fatal("folder browser must sort names and preserve exact paths")
	}
	for _, bad := range []string{"relative-folder", filepath.Join(root, "missing"), filepath.Join(root, "file.txt")} {
		if _, err := listLocalFolders(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

type folderReader struct {
	entries []reconcile.RemoteEntry
	err     error
}

func (f folderReader) ListDir(string) ([]reconcile.RemoteEntry, error) { return f.entries, f.err }
func (f folderReader) Meta(string) (reconcile.RemoteEntry, error) {
	panic("folder browsing must only list directories")
}

func TestRemoteFolderBrowserNavigationAndRootProtection(t *testing.T) {
	fs := folderReader{entries: []reconcile.RemoteEntry{
		{Path: "/备份/中文 (资料)", Name: "中文 (资料)", IsDir: true},
		{Path: "/备份/file.txt", Name: "file.txt"},
		{Path: "/outside", Name: "outside", IsDir: true},
	}}
	listing, err := listRemoteFolders(fs, "/备份")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Parent != "/" || !listing.Selectable || len(listing.Folders) != 1 || listing.Folders[0].Path != "/备份/中文 (资料)" {
		t.Fatalf("unexpected cloud folder listing: %+v", listing)
	}
	root, err := listRemoteFolders(folderReader{}, "")
	if err != nil || root.Path != "/" || root.Selectable || root.Folders == nil {
		t.Fatal("cloud root must be browsable but not selectable")
	}
	if _, err := listRemoteFolders(fs, "relative"); err == nil {
		t.Fatal("relative cloud paths must be rejected")
	}
	if _, err := listRemoteFolders(folderReader{err: errors.New("offline")}, "/备份"); err == nil {
		t.Fatal("listing failures must not look like empty directories")
	}
}
