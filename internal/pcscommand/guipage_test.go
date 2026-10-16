package pcscommand

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile"
)

// The browser must send the original token, without an extra layer of quotes.
// html/template already serializes strings in JavaScript expressions.
func TestGuiPageTokenAuthenticatesAPI(t *testing.T) {
	for _, token := range []string{"0123456789abcdef0123456789abcdef", "quote\"slash\\</script>"} {
		page := renderGuiPage(token, "", "")
		const prefix = "const TOKEN = "
		_, after, found := strings.Cut(page, prefix)
		if !found {
			t.Fatal("page has no token assignment")
		}
		literal, _, _ := strings.Cut(after, ";\n")
		var browserToken string
		if err := json.Unmarshal([]byte(literal), &browserToken); err != nil {
			t.Fatalf("invalid token expression: %v", err)
		}
		session := &GuiSession{token: token}
		called := false
		handler := session.protected(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/api/state?token="+url.QueryEscape(browserToken), nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK || !called {
			t.Fatalf("rendered page cannot authenticate: HTTP %d", rec.Code)
		}
	}
}

func TestGuiEmptyLogIsArray(t *testing.T) {
	session := &GuiSession{}
	rec := httptest.NewRecorder()
	session.handleLog(rec, httptest.NewRequest(http.MethodGet, "/api/log", nil))
	var payload struct {
		OK    bool     `json:"ok"`
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Lines == nil || len(payload.Lines) != 0 {
		t.Fatal("empty log must return an empty array for the page to read its length")
	}
}

func TestGuiPanicReturnsJSONAndInvalidatesPlan(t *testing.T) {
	session := &GuiSession{token: "test-token", plan: &reconcile.Plan{}}
	server := httptest.NewServer(session.protected(session.operation(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated upload panic")
	})))
	defer server.Close()
	res, err := http.Post(server.URL+"/?token=test-token", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("panic must return JSON instead of dropping the connection: %v", err)
	}
	defer res.Body.Close()
	var payload struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError || payload.OK || payload.Error == "" {
		t.Fatal("panic must produce a clear error response")
	}
	if session.plan != nil || !session.workMu.TryLock() {
		t.Fatal("panic must invalidate the plan and release the operation lock")
	}
	session.workMu.Unlock()
}

func TestGuiRejectsOverlappingCloudOperations(t *testing.T) {
	session := &GuiSession{token: "test-token"}
	session.workMu.Lock()
	defer session.workMu.Unlock()
	called := false
	handler := session.protected(session.operation(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/api/apply?token=test-token", strings.NewReader("{}")))
	if rec.Code != http.StatusConflict || called {
		t.Fatal("a second operation must be rejected while the first is running")
	}
}

func TestGuiFailedApplyConsumesPlan(t *testing.T) {
	session := &GuiSession{plan: &reconcile.Plan{}}
	rec := httptest.NewRecorder()
	session.handleApply(rec, httptest.NewRequest(http.MethodPost, "/api/apply", strings.NewReader("{}")))
	if session.plan != nil {
		t.Fatal("failed apply must require a fresh preview")
	}
	data, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(data), `"ok":false`) {
		t.Fatal("invalid test plan must not execute")
	}
}
