package reconcile

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsfunctions/pcsupload"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/checksum"
	"github.com/qjfoidnh/BaiduPCS-Go/pcsutil/taskframework"
)

// RemoteWriter is the mutating cloud surface, implemented over the upstream
// client. All write operations live in this file; the planning code only ever
// sees Reader, so it cannot mutate anything.
type RemoteWriter struct {
	PCS *baidupcs.BaiduPCS
	// Retry controls backoff and pacing for every request.
	Retry RetryPolicy
	// Sleep is the backoff sleep (test seam). Nil means time.Sleep.
	Sleep func(time.Duration)
	// Log receives retry/limit notices. Nil disables logging.
	Log func(string)
	// Parallel and MaxRetry configure the upload unit.
	Parallel int
	MaxRetry int

	lastRequest time.Time
}

// NewRemoteWriter wraps an authenticated upstream client.
func NewRemoteWriter(pcs *baidupcs.BaiduPCS) *RemoteWriter {
	return &RemoteWriter{PCS: pcs, Parallel: 4, MaxRetry: 3}
}

func (w *RemoteWriter) sleep(d time.Duration) {
	if w.Sleep != nil {
		w.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (w *RemoteWriter) logf(format string, a ...interface{}) {
	if w.Log != nil {
		w.Log(fmt.Sprintf(format, a...))
	}
}

func (w *RemoteWriter) pace() {
	policy := w.Retry.withDefaults()
	if policy.Spacing <= 0 {
		return
	}
	if !w.lastRequest.IsZero() {
		if wait := policy.Spacing - time.Since(w.lastRequest); wait > 0 {
			w.sleep(wait)
		}
	}
	w.lastRequest = time.Now()
}

// retry runs a cloud call with the shared backoff policy, returning the number
// of attempts so callers can tell throttling from a persistent failure.
func (w *RemoteWriter) retry(op string, fn func() error) (int, error) {
	policy := w.Retry.withDefaults()
	err, attempts, exhausted := withRetry(policy, w.sleep, w.pace, fn)
	if err != nil && attempts > 1 {
		w.logf("%s 重试 %d 次后仍失败: %v", op, attempts, err)
	}
	if err != nil && exhausted && IsAuthError(err) {
		return attempts, &AuthExpiredError{At: op, Cause: err}
	}
	return attempts, err
}

// Mkdir implements Writer.
func (w *RemoteWriter) Mkdir(remotePath string) error {
	if w == nil || w.PCS == nil {
		return ErrNoClient
	}
	_, err := w.retry("创建目录 "+remotePath, func() error {
		if pcsErr := w.PCS.Mkdir(remotePath); pcsErr != nil {
			return wrapPCSError("创建目录", remotePath, pcsErr)
		}
		return nil
	})
	return err
}

// ListDir implements Writer by delegating to the same retrying reader.
func (w *RemoteWriter) ListDir(remotePath string) ([]RemoteEntry, error) {
	fs := &RemoteFS{PCS: w.PCS, Retry: w.Retry, Sleep: w.Sleep, Log: w.Log}
	return fs.ListDir(remotePath)
}

// Meta implements Writer by delegating to the same retrying reader.
func (w *RemoteWriter) Meta(remotePath string) (RemoteEntry, error) {
	fs := &RemoteFS{PCS: w.PCS, Retry: w.Retry, Sleep: w.Sleep, Log: w.Log}
	return fs.Meta(remotePath)
}

// Move implements Writer.
func (w *RemoteWriter) Move(from, to string) error {
	if w == nil || w.PCS == nil {
		return ErrNoClient
	}
	if err := assertInsideRoot("/", from); err != nil {
		return err
	}
	_, err := w.retry("移动 "+from, func() error {
		if pcsErr := w.PCS.Move(&baidupcs.CpMvJSON{From: from, To: to}); pcsErr != nil {
			return wrapPCSError("移动", from, pcsErr)
		}
		return nil
	})
	return err
}

// Remove implements Writer. It deletes into the provider's recycle bin, which
// is the only removal upstream exposes; permanent deletion is never used.
func (w *RemoteWriter) Remove(remotePaths ...string) error {
	if w == nil || w.PCS == nil {
		return ErrNoClient
	}
	if len(remotePaths) == 0 {
		return nil
	}
	_, err := w.retry(fmt.Sprintf("删除 %d 项", len(remotePaths)), func() error {
		if pcsErr := w.PCS.Remove(remotePaths...); pcsErr != nil {
			return wrapPCSError("删除", remotePaths[0], pcsErr)
		}
		return nil
	})
	return err
}

// Upload implements Writer by driving the project's own upload task unit, so
// rapid-upload, chunking, hashing and retry behaviour are identical to the
// existing `upload` command rather than a second implementation.
//
// Two upstream gaps are handled here explicitly:
//
//   - checksum.GetFileSum returns nil hashes for a zero-byte file, so the
//     hashes are computed here instead.
//   - the upload path does not check whether the target is a directory, so the
//     executor verifies that before calling this.
func (w *RemoteWriter) Upload(localPath, remotePath string, size int64) error {
	if w == nil || w.PCS == nil {
		return ErrNoClient
	}

	sum, err := computeUploadSum(localPath)
	if err != nil {
		return err
	}
	defer sum.Close()

	unit := &pcsupload.UploadTaskUnit{
		LocalFileChecksum: sum,
		SavePath:          remotePath,
		PCS:               w.PCS.CopyPCS(),
		Parallel:          w.Parallel,
		Policy:            baidupcs.OverWritePolicy,
		UploadStatistic:   &pcsupload.UploadStatistic{},
		PrintFormat:       pcsupload.DefaultPrintFormat,
	}
	// The unit dereferences its task info, which the executor attaches.
	taskframework.NewTaskExecutor().Append(unit, w.MaxRetry)

	// The unit also dereferences its resume database unconditionally, so a real
	// (but deliberately EMPTY) store is supplied:
	//
	//   * nil panics inside UploadingDatabase.Search;
	//   * the user's own database must not be used, because a stale record from
	//     an earlier interrupted run makes the unit take its resume branch, which
	//     neither verifies the remote content nor re-uploads it - reconciliation
	//     would then report success for a file it never sent.
	//
	// An empty store gives the unit the object it requires while guaranteeing no
	// stale state can influence the result.
	db, cleanup, err := newEmptyUploadingDatabase()
	if err != nil {
		return fmt.Errorf("初始化上传状态库失败, 未开始上传: %w", err)
	}
	unit.UploadingDatabase = db
	defer cleanup()

	w.pace()
	result := unit.Run()
	if result == nil {
		return fmt.Errorf("上传 %s 未返回结果", remotePath)
	}
	if result.Err != nil {
		return fmt.Errorf("上传 %s 失败: %w", remotePath, result.Err)
	}
	if !result.Succeed {
		return fmt.Errorf("上传 %s 未成功: %s", remotePath, result.ResultMessage)
	}
	return nil
}

// computeUploadSum builds a LocalFileChecksum with hashes that are correct for
// empty files too.
func computeUploadSum(localPath string) (*checksum.LocalFileChecksum, error) {
	sum := checksum.NewLocalFileChecksum(localPath, int(baidupcs.SliceMD5Size))
	if err := sum.OpenPath(); err != nil {
		return nil, err
	}
	defer sum.Close()
	f := sum.GetFile()

	full := md5.New()
	slice := md5.New()
	const sliceSize = int64(baidupcs.SliceMD5Size)
	buf := make([]byte, 1<<20)
	var read int64
	for {
		n, rErr := f.Read(buf)
		if n > 0 {
			full.Write(buf[:n])
			if read < sliceSize {
				remain := sliceSize - read
				if int64(n) <= remain {
					slice.Write(buf[:n])
				} else {
					slice.Write(buf[:remain])
				}
				read += int64(n)
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return nil, rErr
		}
	}
	sum.MD5 = full.Sum(nil)
	sum.SliceMD5 = slice.Sum(nil)
	if err := sum.CalculateChunkedSum(uploadBlockSize(sum.Length)); err != nil {
		return nil, err
	}
	return sum, nil
}

// uploadBlockSize mirrors upstream's unexported block-size choice.
func uploadBlockSize(size int64) int64 {
	switch {
	case size >= baidupcs.MaxUploadThreshold:
		return baidupcs.MaxUploadBlockSize
	case size >= baidupcs.MiddleUploadThreshold:
		return baidupcs.MiddleUploadBlockSize
	default:
		return baidupcs.MinUploadBlockSize
	}
}

// newEmptyUploadingDatabase builds an upload-state store backed by a private
// temporary file, which the upload unit requires but which must carry no records.
//
// The file is removed when the database is closed, so a run leaves no state
// behind for the next one.
func newEmptyUploadingDatabase() (*pcsupload.UploadingDatabase, func(), error) {
	// The config directory is already writable for login state. Windows TEMP
	// may belong to a different privilege context or have unusable permissions.
	f, err := os.CreateTemp(pcsconfig.GetConfigDir(), "reconcile-uploadstate-*.json")
	if err != nil {
		return nil, nil, err
	}
	name := f.Name()
	f.Close()

	// NewUploadingDatabase reads from the config directory; the same shape is
	// reproduced here against a private path.
	db, err := pcsupload.NewUploadingDatabaseAt(name)
	if err != nil {
		os.Remove(name)
		return nil, nil, err
	}
	return db, func() {
		db.Close()
		os.Remove(name)
	}, nil
}
