package baidupcs

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type reconcileTransport func(*http.Request) (*http.Response, error)

func (f reconcileTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testPCSWithTransport(fn reconcileTransport) *BaiduPCS {
	pcs := NewPCS(0, "")
	client := pcs.GetClient()
	client.SetHTTPSecure(true)
	client.Transport = fn
	return pcs
}

func TestListingRejectsRepeatedPageRatherThanReturningPartialSuccess(t *testing.T) {
	var entries []string
	for i := 1; i <= 1000; i++ {
		entries = append(entries, fmt.Sprintf(`{"fs_id":%d,"path":"/backup/f%d","server_filename":"f%d","size":1,"isdir":0}`, i, i, i))
	}
	page := `{"errno":0,"list":[` + strings.Join(entries, ",") + `]}`
	calls := 0
	pcs := testPCSWithTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(page)), Header: make(http.Header), Request: r}, nil
	})
	paced := 0
	_, err := pcs.FilesDirectoriesListWithPacing("/backup", DefaultOrderOptions, func() { paced++ })
	if err == nil || calls != 2 || paced != calls {
		t.Fatalf("must reject a looping page, got calls=%d err=%v", calls, err)
	}
}

func TestCopyAndMovePropagatePreparationErrors(t *testing.T) {
	pcs := testPCSWithTransport(func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("simulated transport failure") })
	op := &CpMvJSON{From: "/backup/a", To: "/backup/b"}
	if err := pcs.Copy(op); err == nil {
		t.Fatal("copy preparation failure must not report success")
	}
	if err := pcs.Move(op); err == nil {
		t.Fatal("move preparation failure must not report success")
	}
}
