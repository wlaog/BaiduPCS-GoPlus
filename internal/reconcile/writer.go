package reconcile

import (
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"sync"
	"time"
)

// RemoteWriter exposes only same-directory moves and recycle-bin deletion.
// Mutations are single attempts: Executor retries with fresh state checks.
type RemoteWriter struct {
	PCS          *baidupcs.BaiduPCS
	Retry        RetryPolicy
	Sleep        func(time.Duration)
	Log          func(string)
	paceMu, opMu sync.Mutex
	lastRequest  time.Time
}

func NewRemoteWriter(pcs *baidupcs.BaiduPCS) *RemoteWriter {
	return &RemoteWriter{PCS: pcs, Retry: DefaultRetryPolicy()}
}
func (w *RemoteWriter) sleep(d time.Duration) {
	if w.Sleep != nil {
		w.Sleep(d)
	} else {
		time.Sleep(d)
	}
}
func (w *RemoteWriter) pace() {
	w.paceMu.Lock()
	defer w.paceMu.Unlock()
	spacing := w.Retry.withDefaults().Spacing
	if spacing > 0 && !w.lastRequest.IsZero() {
		if wait := spacing - time.Since(w.lastRequest); wait > 0 {
			w.sleep(wait)
		}
	}
	w.lastRequest = time.Now()
}
func (w *RemoteWriter) ListDir(p string) ([]RemoteEntry, error) {
	return (&RemoteFS{PCS: w.PCS, Retry: w.Retry, Sleep: w.Sleep, Log: w.Log, BeforeRequest: w.pace}).ListDir(p)
}
func (w *RemoteWriter) Meta(p string) (RemoteEntry, error) {
	return (&RemoteFS{PCS: w.PCS, Retry: w.Retry, Sleep: w.Sleep, Log: w.Log, BeforeRequest: w.pace}).Meta(p)
}
func (w *RemoteWriter) Move(from, to string) error {
	if w == nil || w.PCS == nil {
		return ErrNoClient
	}
	w.opMu.Lock()
	defer w.opMu.Unlock()
	w.pace()
	if err := w.PCS.CopyPCS().Move(&baidupcs.CpMvJSON{From: from, To: to}); err != nil {
		return wrapPCSError("重命名", from, err)
	}
	return nil
}
func (w *RemoteWriter) Remove(paths ...string) error {
	if w == nil || w.PCS == nil {
		return ErrNoClient
	}
	if len(paths) == 0 {
		return nil
	}
	w.opMu.Lock()
	defer w.opMu.Unlock()
	w.pace()
	if err := w.PCS.CopyPCS().Remove(paths...); err != nil {
		return wrapPCSError("移入回收站", paths[0], err)
	}
	return nil
}
