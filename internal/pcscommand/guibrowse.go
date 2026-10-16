package pcscommand

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile"
)

type guiFolder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type guiFolders struct {
	Path       string      `json:"path"`
	Parent     string      `json:"parent"`
	Selectable bool        `json:"selectable"`
	Folders    []guiFolder `json:"folders"`
}

func (s *GuiSession) handleBrowse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		s.fail(w, errors.New("无法读取目录选择请求"))
		return
	}
	var listing *guiFolders
	var err error
	switch req.Kind {
	case "local":
		listing, err = listLocalFolders(req.Path)
	case "remote":
		if _, err = ensureLogin(); err == nil {
			listing, err = listRemoteFolders(reconcile.NewRemoteFS(GetBaiduPCS()), req.Path)
		}
	default:
		err = errors.New("请选择本地或云端文件夹")
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, map[string]interface{}{"ok": true, "listing": listing})
}

func listLocalFolders(dir string) (*guiFolders, error) {
	out := &guiFolders{Folders: []guiFolder{}}
	if dir == "" {
		for _, root := range localFolderRoots() {
			out.Folders = append(out.Folders, guiFolder{Name: root, Path: root})
		}
		return out, nil
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("请输入完整的本地路径，例如 D:\\WorkSync")
	}
	dir = filepath.Clean(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("无法打开本地文件夹: %w", err)
	}
	out.Path, out.Parent, out.Selectable = dir, filepath.Dir(dir), true
	if out.Parent == dir {
		out.Parent = "" // back to the drive list
	}
	for _, entry := range entries {
		// Match reconciliation's policy: do not browse through links/junctions.
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			out.Folders = append(out.Folders, guiFolder{Name: entry.Name(), Path: filepath.Join(dir, entry.Name())})
		}
	}
	sortFolders(out.Folders)
	return out, nil
}

func listRemoteFolders(fs reconcile.Reader, dir string) (*guiFolders, error) {
	if dir == "" {
		dir = "/"
	}
	if !strings.HasPrefix(dir, "/") || strings.ContainsRune(dir, '\x00') {
		return nil, errors.New("云端路径应以 / 开头")
	}
	dir = path.Clean(dir)
	entries, err := fs.ListDir(dir)
	if err != nil {
		return nil, err
	}
	out := &guiFolders{Path: dir, Parent: path.Dir(dir), Selectable: dir != "/", Folders: []guiFolder{}}
	for _, entry := range entries {
		if entry.IsDir && path.Dir(entry.Path) == dir && path.Base(entry.Path) == entry.Name && entry.Name != "." && entry.Name != ".." {
			out.Folders = append(out.Folders, guiFolder{Name: entry.Name, Path: entry.Path})
		}
	}
	sortFolders(out.Folders)
	return out, nil
}

func sortFolders(folders []guiFolder) {
	sort.Slice(folders, func(i, j int) bool {
		return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
	})
}
