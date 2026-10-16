package reconcile

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
)

// HashCache is only a performance hint. Cross-run reuse is opt-in because
// size/mtime cannot prove that content is unchanged. Strict scans ignore it.
type HashCache struct {
	mu      sync.Mutex
	Entries map[string]CloudContentHash `json:"entries"`
}

func LoadHashCache(name string) *HashCache {
	c := &HashCache{Entries: make(map[string]CloudContentHash)}
	b, err := os.ReadFile(name)
	if err == nil {
		_ = json.Unmarshal(b, c)
	}
	if c.Entries == nil {
		c.Entries = make(map[string]CloudContentHash)
	}
	return c
}

func (c *HashCache) Save(name string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	b, err := json.Marshal(c)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".reconcile-hashes-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), name)
}

func (c *HashCache) get(name string, info os.FileInfo) (CloudContentHash, bool) {
	if c == nil {
		return CloudContentHash{}, false
	}
	c.mu.Lock()
	h, ok := c.Entries[name]
	c.mu.Unlock()
	return h, ok && h.matches(info) && looksLikeMD5(h.MD5)
}

func (c *HashCache) put(name string, h CloudContentHash) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Entries == nil {
		c.Entries = make(map[string]CloudContentHash)
	}
	c.Entries[name] = h
}

func (h CloudContentHash) matches(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() == h.Size && info.ModTime().UnixNano() == h.MtimeNano
}

// hashFile reads content once for version matching. The prefix checksum is
// retained for the read-only probe; no upload block hashes are calculated.
func hashFile(name string, progress func(int64)) (CloudContentHash, error) {
	var out CloudContentHash
	f, err := os.Open(name)
	if err != nil {
		return out, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return out, err
	}
	if !before.Mode().IsRegular() {
		return out, fmt.Errorf("不是普通文件: %s", name)
	}
	out.Size, out.Mtime, out.MtimeNano = before.Size(), before.ModTime().Unix(), before.ModTime().UnixNano()
	full, slice := md5.New(), md5.New()
	buf := make([]byte, 1<<20)
	var read int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			full.Write(buf[:n])
			if read < baidupcs.SliceMD5Size {
				end := int64(n)
				if remaining := int64(baidupcs.SliceMD5Size) - read; end > remaining {
					end = remaining
				}
				slice.Write(buf[:end])
			}

			read += int64(n)
			if progress != nil {
				progress(read)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return CloudContentHash{}, rerr
		}
	}
	after, err := f.Stat()
	if err != nil {
		return CloudContentHash{}, err
	}
	live, err := os.Stat(name)
	if err != nil {
		return CloudContentHash{}, err
	}
	if read != out.Size || !out.matches(after) || !out.matches(live) || !os.SameFile(before, live) {
		return CloudContentHash{}, fmt.Errorf("文件在读取期间发生变化，请重新预览: %s", name)
	}
	out.MD5, out.SliceMD5 = hex.EncodeToString(full.Sum(nil)), hex.EncodeToString(slice.Sum(nil))
	return out, nil
}
