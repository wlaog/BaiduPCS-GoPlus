package pcscommand

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/qjfoidnh/BaiduPCS-Go/internal/reconcile"
)

// GuiOptions configures the local graphical front-end.
type GuiOptions struct {
	// Addr is the listen address. Empty means 127.0.0.1 on an ephemeral port.
	Addr string
	// OpenBrowser launches the default browser at the tokenised URL.
	OpenBrowser bool
	// LocalDir and RemoteRoot pre-fill the form.
	LocalDir   string
	RemoteRoot string
}

// GuiSession is the state the browser talks to.
//
// The server exists only to render and drive the same three operations the CLI
// exposes. It holds no cloud credentials of its own: it uses the already
// configured account through the same calls the command line makes.
type GuiSession struct {
	options GuiOptions
	token   string
	// quit is closed when the page asks the server to stop.
	quit chan struct{}

	mu              sync.Mutex
	workMu          sync.Mutex
	log             []string
	plan            *reconcile.Plan
	report          *reconcile.PlanReport
	progress        guiProgress
	lastProgressLog time.Time
}

type guiProgress struct {
	reconcile.Progress
	Running   bool   `json:"running"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt"`
}

// guiVersion identifies the front-end in reports and in the page footer.
const guiVersion = "0.4.1"

// RunReconcileGui starts the local interface and blocks until the process is
// interrupted or the page asks it to shut down.
func RunReconcileGui(opt *GuiOptions) error {
	if opt == nil {
		opt = &GuiOptions{}
	}
	if !opt.OpenBrowser && !isFlagPassed("no-browser") {
		opt.OpenBrowser = true
	}
	if opt.Addr == "" {
		opt.Addr = "127.0.0.1:0"
	}

	// A loopback bind plus an unguessable path token keeps other local
	// processes (and any web page the user happens to have open) from driving
	// this API. The token is required on every request, not just the first.
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("生成访问令牌失败: %w", err)
	}
	session := &GuiSession{options: *opt, token: hex.EncodeToString(tokenBytes)}

	ln, err := net.Listen("tcp", opt.Addr)
	if err != nil {
		return fmt.Errorf("无法监听 %s: %w", opt.Addr, err)
	}
	// Refuse to be reachable off-host even if the caller passed 0.0.0.0.
	if host, _, splitErr := net.SplitHostPort(ln.Addr().String()); splitErr == nil {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			ln.Close()
			return fmt.Errorf("拒绝绑定非本机地址 %s: 界面只在本机可用", host)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", session.handleIndex)
	mux.HandleFunc("/api/state", session.protected(session.handleState))
	mux.HandleFunc("/api/probe", session.protected(session.operation(session.handleProbe)))
	mux.HandleFunc("/api/plan", session.protected(session.operation(session.handlePlan)))
	mux.HandleFunc("/api/apply", session.protected(session.operation(session.handleApply)))
	mux.HandleFunc("/api/log", session.protected(session.handleLog))
	mux.HandleFunc("/api/browse", session.protected(session.operation(session.handleBrowse)))
	mux.HandleFunc("/api/quit", session.protected(session.handleQuit))

	srv := &http.Server{Handler: mux}
	url := fmt.Sprintf("http://%s/?token=%s", ln.Addr().String(), session.token)

	fmt.Printf("对齐本地 · 图形界面已启动\n")
	fmt.Printf("  地址: %s\n", url)
	fmt.Printf("  说明: 只监听本机, 每次访问都需要令牌; 关闭本窗口或按 Ctrl+C 结束\n")
	fmt.Printf("  本界面只驱动 reconcile 的 探测/预览/执行 三个操作, 不替代官方客户端。\n\n")

	quit := make(chan struct{})
	session.quit = quit
	if opt.OpenBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Printf("未能自动打开浏览器 (%v), 请手动访问上面的地址。\n", err)
		}
	}

	go func() {
		<-quit
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	fmt.Println("界面已关闭。")
	return nil
}

// protected wraps a handler with the token check.
func (s *GuiSession) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != s.token {
			http.Error(w, "无效的访问令牌", http.StatusForbidden)
			return
		}
		// A browser on another origin must not be able to drive this API.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if p := recover(); p != nil {
				s.mu.Lock()
				s.plan = nil
				s.mu.Unlock()
				// Do not print panic values, which could contain credentials.
				fmt.Fprintf(os.Stderr, "GUI request panic (%T):\n%s", p, debug.Stack())
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				s.fail(w, errors.New("操作异常中止，部分云端操作可能已完成。请查看日志并重新预览差异后再执行，不要直接重试旧计划"))
			}
		}()
		next(w, r)
	}
}

// Only one cloud operation may run at a time; state/log requests stay available.
func (s *GuiSession) operation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusMethodNotAllowed)
			s.fail(w, errors.New("此操作需要 POST 请求"))
			return
		}
		if !s.workMu.TryLock() {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			s.fail(w, errors.New("已有操作正在进行，请等待完成后再试"))
			return
		}
		defer s.workMu.Unlock()
		tracked := r.URL.Path == "/api/plan" || r.URL.Path == "/api/probe" || r.URL.Path == "/api/apply"
		if tracked {
			s.mu.Lock()
			s.progress = guiProgress{Running: true, Status: "running", StartedAt: time.Now().UnixMilli(), Progress: reconcile.Progress{Phase: "starting"}}
			s.lastProgressLog = time.Time{}
			s.mu.Unlock()
			defer func() {
				s.mu.Lock()
				s.progress.Running = false
				if s.progress.Status == "running" {
					s.progress.Status = "complete"
				}
				s.mu.Unlock()
			}()
		}
		next(w, r)
	}
}

func (s *GuiSession) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("token") != s.token {
		http.Error(w, "无效的访问令牌: 请使用启动时打印的完整地址", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, renderGuiPage(s.token, s.options.LocalDir, s.options.RemoteRoot))
}

func (s *GuiSession) appendLog(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, fmt.Sprintf("%s  %s", time.Now().Format("15:04:05"), line))
	if len(s.log) > 500 {
		s.log = s.log[len(s.log)-500:]
	}
}

func (s *GuiSession) writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func (s *GuiSession) fail(w http.ResponseWriter, err error) {
	s.mu.Lock()
	s.progress.Status = "error"
	s.mu.Unlock()
	s.appendLog("错误: " + err.Error())
	s.writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
}

func (s *GuiSession) handleState(w http.ResponseWriter, r *http.Request) {
	sess := &ReconcileSession{Observer: s.appendLog, Progress: s.observeProgress}
	s.mu.Lock()
	pending := s.report
	if s.plan == nil {
		pending = nil
	}
	s.mu.Unlock()
	s.writeJSON(w, map[string]interface{}{
		"ok":          true,
		"version":     guiVersion,
		"account":     sess.Account(),
		"pendingPlan": pending,
	})
}

func (s *GuiSession) handleLog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	lines := append([]string{}, s.log...)
	progress := s.progress
	s.mu.Unlock()
	s.writeJSON(w, map[string]interface{}{"ok": true, "lines": lines, "progress": progress})
}

func (s *GuiSession) observeProgress(p reconcile.Progress) {
	s.mu.Lock()
	phaseChanged := s.progress.Phase != p.Phase
	s.progress.Progress = p
	printLine := phaseChanged || time.Since(s.lastProgressLog) > 2*time.Second
	if printLine {
		s.lastProgressLog = time.Now()
	}
	s.mu.Unlock()
	if printLine {
		label := map[string]string{"rename": "重命名", "local": "扫描本地", "cloud": "扫描云端", "hash": "核对内容", "hash-upload": "准备上传", "transfer": "正在上传", "upload": "文件处理", "verify": "核验写入", "mkdir": "创建目录", "remove": "清理多余项", "planned": "预览完成", "recheck": "执行前复核", "recheck-remove": "清理前复核"}[p.Phase]
		if label == "" {
			label = p.Phase
		}
		line := label
		if p.Total > 0 && p.Phase != "transfer" && p.Phase != "hash-upload" {
			line += fmt.Sprintf(" %d/%d", p.Done, p.Total)
		}
		if p.Current != "" {
			line += " · " + p.Current
		}
		s.appendLog(line)
	}
}

func (s *GuiSession) handleQuit(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, map[string]interface{}{"ok": true})
	if s.quit != nil {
		close(s.quit)
	}
}

func (s *GuiSession) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemoteRoot    string `json:"remoteRoot"`
		LocalDir      string `json:"localDir"`
		HashSamples   int    `json:"hashSamples"`
		DiscoverRoots bool   `json:"discoverRoots"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, errors.New("请求格式错误: "+err.Error()))
		return
	}
	sess := &ReconcileSession{Observer: s.appendLog, Progress: s.observeProgress}
	report, err := sess.Probe(ReconcileProbeOptions{
		RemoteRoot:    req.RemoteRoot,
		LocalDir:      req.LocalDir,
		HashSamples:   req.HashSamples,
		DiscoverRoots: req.DiscoverRoots,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	path, saveErr := SaveProbeReport(report)
	if len(report.Errors) > 0 {
		s.mu.Lock()
		s.progress.Status = "warning"
		s.mu.Unlock()
	}
	if saveErr != nil {
		s.appendLog("警告: 未能保存探测报告: " + saveErr.Error())
	} else {
		s.appendLog("探测报告已保存: " + path)
	}
	s.writeJSON(w, map[string]interface{}{"ok": true, "report": report, "savedTo": path})
}

func (s *GuiSession) handlePlan(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.plan = nil
	s.mu.Unlock()
	var req struct {
		LocalDir     string `json:"localDir"`
		RemoteRoot   string `json:"remoteRoot"`
		HashFiles    bool   `json:"hashFiles"`
		Conservative bool   `json:"conservative"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, errors.New("请求格式错误: "+err.Error()))
		return
	}
	sess := &ReconcileSession{Observer: s.appendLog, Progress: s.observeProgress}
	report, plan, err := sess.Plan(ReconcilePlanOptions{
		LocalRoot:    req.LocalDir,
		RemoteRoot:   req.RemoteRoot,
		HashFiles:    req.HashFiles,
		Conservative: req.Conservative,
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	s.mu.Lock()
	s.plan = plan
	s.report = report
	if !plan.ScanComplete || !plan.LocalScanComplete {
		s.progress.Status = "warning"
	}
	s.mu.Unlock()

	path, saveErr := SavePlanReport(report)
	if saveErr != nil {
		s.appendLog("警告: 未能保存差异报告: " + saveErr.Error())
	} else {
		s.appendLog("差异报告已保存: " + path)
	}
	s.writeJSON(w, map[string]interface{}{"ok": true, "report": report, "savedTo": path})
}

func (s *GuiSession) handleApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemovalMode        string  `json:"removalMode"`
		QuarantineRoot     string  `json:"quarantineRoot"`
		ConfirmRemoval     bool    `json:"confirmRemoval"`
		AcknowledgeRecycle bool    `json:"acknowledgeRecycle"`
		AcknowledgeBlast   bool    `json:"acknowledgeBlast"`
		MaxRemovePercent   float64 `json:"maxRemovePercent"`
		MaxRemoveBytes     int64   `json:"maxRemoveBytes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, errors.New("请求格式错误: "+err.Error()))
		return
	}

	s.mu.Lock()
	plan := s.plan
	// An attempted apply may have partially changed the cloud, even on failure.
	s.plan = nil
	s.mu.Unlock()
	if plan == nil {
		s.fail(w, errors.New("请先执行「预览差异」。执行前必须有一次针对当前目录的预览, 否则不会做任何改动"))
		return
	}

	sess := &ReconcileSession{Observer: s.appendLog, Progress: s.observeProgress}
	report, err := sess.Apply(ApplyOptions{
		Plan:               plan,
		RemovalMode:        req.RemovalMode,
		QuarantineRoot:     req.QuarantineRoot,
		ConfirmRemoval:     req.ConfirmRemoval,
		AcknowledgeRecycle: req.AcknowledgeRecycle,
		AcknowledgeBlast:   req.AcknowledgeBlast,
		MaxRemovePercent:   req.MaxRemovePercent,
		MaxRemoveBytes:     req.MaxRemoveBytes,
	})
	if err != nil {
		// All executor errors before item results are preflight failures. Keep
		// the expensive preview available after login or local access recovers.
		if report == nil || len(report.Renames)+len(report.Removes) == 0 {
			s.mu.Lock()
			s.plan = plan
			s.report = reconcile.BuildPlanReport(plan, "执行前检查未通过，保留预览")
			s.mu.Unlock()
		} else {
			s.finishApply(plan, report)
		}
		s.mu.Lock()
		remaining := s.report
		s.mu.Unlock()
		s.appendLog("执行未开始或未完成: " + err.Error())
		s.mu.Lock()
		s.progress.Status = "error"
		s.mu.Unlock()
		s.writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error(), "remainingReport": remaining})
		return
	}
	remaining := s.finishApply(plan, report)
	path, saveErr := SaveApplyReport(report)
	if saveErr != nil {
		s.appendLog("警告: 未能保存执行记录: " + saveErr.Error())
	} else {
		s.appendLog("执行记录已保存: " + path)
	}
	s.appendLog("本次可执行项已处理；失败项可单独重试，其他项不会重复执行")

	s.writeJSON(w, map[string]interface{}{"ok": true, "report": report.ToJSON(), "savedTo": path, "remainingReport": remaining})
}

func (s *GuiSession) finishApply(plan *reconcile.Plan, report *reconcile.ApplyReport) *reconcile.PlanReport {
	remaining := reconcile.RemainingPlan(plan, report)
	var preview *reconcile.PlanReport
	if remaining != nil {
		preview = reconcile.BuildPlanReport(remaining, "仅重试未完成项（无需重新全盘扫描）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plan = remaining
	s.report = preview
	if !report.Succeeded() {
		s.progress.Status = "warning"
	}
	return preview
}

// isFlagPassed reports whether a CLI flag is present in os.Args.
func isFlagPassed(name string) bool {
	for _, a := range os.Args[1:] {
		if strings.TrimLeft(a, "-") == name || strings.HasPrefix(a, "--"+name+"=") || strings.HasPrefix(a, "-"+name+"=") {
			return true
		}
	}
	return false
}
