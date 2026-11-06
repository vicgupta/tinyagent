package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// tinyagent web: the chat session (chat_session.go) behind a local HTTP
// server and a browser UI. The UI is plain HTML, CSS and JavaScript embedded
// in the binary; events stream to it over Server-Sent Events and it acts
// with JSON POSTs. Every request must carry the per-run token and a
// loopback Host, so other local sites and DNS rebinding can't drive it.

//go:embed web
var webAssets embed.FS

// defaultWebPort is tinyagent web's port unless --port is given.
const defaultWebPort = 7777

// webConflicts are the flags --web can't be combined with.
var webConflicts = append([]string{"interactive", "i"}, chatConflicts...)

func webConflict(fs *flag.FlagSet) string {
	var list []string
	for _, f := range webConflicts {
		if f != "web" && f != "web-port" && f != "web-no-open" {
			list = append(list, f)
		}
	}
	return modeConflict(fs, list)
}

// webListen is net.Listen; tests replace it.
var webListen = net.Listen

// listenWeb listens on 127.0.0.1:port. When the port was not set
// explicitly and is busy it tries the next ten, then any free port.
func listenWeb(port int, explicit bool) (net.Listener, error) {
	ln, err := webListen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil || explicit {
		if err != nil {
			return nil, fmt.Errorf("--port %d: %w (pick another port, or leave --port unset to use a free one)", port, err)
		}
		return ln, nil
	}
	for p := port + 1; p <= port+10 && p < 65536; p++ {
		if ln, err := webListen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p))); err == nil {
			return ln, nil
		}
	}
	return webListen("tcp", "127.0.0.1:0")
}

// browserCommand is the command that opens url in the default browser on
// goos.
func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}

// startBrowser runs the opener; tests replace it.
var startBrowser = func(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func openBrowser(url string) error {
	name, args := browserCommand(runtime.GOOS, url)
	return startBrowser(name, args)
}

func newWebToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// runWeb runs tinyagent web until Ctrl-C.
func runWeb(o *options, fs *flag.FlagSet) int {
	errw := os.Stderr
	if o.task == "-" {
		fmt.Fprintln(errw, "error: tinyagent web reads messages from the browser; -t - (task on stdin) can't be used")
		return 2
	}
	if o.webPort < 0 || o.webPort > 65535 {
		fmt.Fprintf(errw, "error: --port %d must be 0-65535\n", o.webPort)
		return 2
	}
	s, prev, code := newChatSession(o, fs, errw)
	if code != 0 {
		return code
	}
	defer s.Close()
	w, err := newWebUI(s, prev, errw)
	if err != nil {
		return fail(err)
	}
	ln, err := listenWeb(o.webPort, flagWasSet(fs, "web-port"))
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	url := w.bind(ln.Addr().(*net.TCPAddr).Port)
	srv := &http.Server{Handler: w, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	fmt.Fprintf(errw, "tinyagent web: %s with %s\n", s.root, s.modelName)
	fmt.Fprintf(errw, "open %s\n", url)
	if !o.webNoOpen {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(errw, "could not open a browser (%v); open the URL above\n", err)
		}
	}
	fmt.Fprintln(errw, "press Ctrl-C to stop")
	select {
	case <-ctx.Done():
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
	}
	fmt.Fprintln(errw, "\nstopping…")
	w.shutdown()
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	if t := w.current(); t != nil {
		fmt.Fprintf(errw, "resume with: tinyagent chat --resume %s (or tinyagent web --resume ...)\n", relTo(s.root, w.path))
	}
	return 0
}

// webEvent is one Server-Sent Event.
type webEvent struct {
	kind string
	data any
}

// webTurnSnap is the conversation before one turn, for edit and
// regenerate of the latest message.
type webTurnSnap struct {
	prev   *trajectory.Trajectory
	source string
	cont   bool
	msg    string
	turns  int
	path   string
}

// webUI is the HTTP handler and the conversation state of one web session.
type webUI struct {
	s     *session
	errw  io.Writer
	token string
	hosts map[string]bool
	files http.Handler

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	turnDone chan struct{}
	path     string
	source   string
	cont     bool
	compact  bool
	turns    int
	last     *webTurnSnap
	subMu    sync.Mutex // guards subs and subsDone; never held with mu
	subs     map[chan webEvent]struct{}
	subsDone bool
	perms    map[string]chan permissions.Answer
	permReqs map[string]map[string]any
	closed   bool
}

func newWebUI(s *session, prev *trajectory.Trajectory, errw io.Writer) (*webUI, error) {
	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		return nil, err
	}
	w := &webUI{s: s, errw: errw, token: newWebToken(), files: http.FileServer(http.FS(sub)),
		subs: map[chan webEvent]struct{}{}, perms: map[string]chan permissions.Answer{}, permReqs: map[string]map[string]any{}}
	w.path = s.o.output
	if w.path == "" {
		if w.path, err = newChatPath(s.root); err != nil {
			return nil, err
		}
	}
	if prev != nil {
		// Like chat, a follow-up continues the checkpoint whatever its
		// status; the browser can't answer chat's "continue first?" prompt.
		s.traj, w.source, w.cont = prev, s.o.resume, true
	}
	return w, nil
}

// bind sets the port the server listens on and returns the URL to open.
func (w *webUI) bind(port int) string {
	p := strconv.Itoa(port)
	w.hosts = map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true, "[::1]:" + p: true}
	return "http://127.0.0.1:" + p + "/?token=" + w.token
}

func (w *webUI) current() *trajectory.Trajectory {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.s.traj
}

// shutdown cancels a running turn, waits for it, and ends the event streams.
func (w *webUI) shutdown() {
	w.mu.Lock()
	cancel, done := w.cancel, w.turnDone
	w.closed = true
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	w.subMu.Lock()
	w.subsDone = true
	for ch := range w.subs {
		close(ch)
		delete(w.subs, ch)
	}
	w.subMu.Unlock()
}

func (w *webUI) broadcast(kind string, data any) {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	for ch := range w.subs {
		select {
		case ch <- webEvent{kind, data}:
		default: // a stalled client drops events rather than the turn
		}
	}
}

// authorized checks the Host header (DNS rebinding), any Origin
// (cross-site requests) and the token.
func (w *webUI) authorized(r *http.Request, needToken bool) (int, string) {
	if !w.hosts[strings.ToLower(r.Host)] {
		return http.StatusForbidden, "forbidden: unexpected Host header"
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
		return http.StatusForbidden, "forbidden: cross-origin request"
	}
	if !needToken {
		return 0, ""
	}
	tok := r.Header.Get("X-TinyAgent-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(w.token)) != 1 {
		return http.StatusUnauthorized, "unauthorized: missing or wrong token; open the URL tinyagent web printed"
	}
	return 0, ""
}

func (w *webUI) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	h := rw.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	static := strings.HasPrefix(r.URL.Path, "/static/")
	if code, msg := w.authorized(r, !static); code != 0 {
		http.Error(rw, msg, code)
		return
	}
	if static {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.StripPrefix("/static", w.files).ServeHTTP(rw, r)
		return
	}
	if r.URL.Path == "/" {
		b, _ := webAssets.ReadFile("web/index.html")
		h.Set("Content-Type", "text/html; charset=utf-8")
		rw.Write(b)
		return
	}
	if r.URL.Path == "/api/events" {
		w.serveEvents(rw, r)
		return
	}
	get := map[string]func() (any, error){
		"/api/state":    func() (any, error) { return w.state(), nil },
		"/api/history":  func() (any, error) { return w.history() },
		"/api/commands": func() (any, error) { return webCommands, nil },
	}
	if f, ok := get[r.URL.Path]; ok {
		if r.Method != http.MethodGet {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		v, err := f()
		writeJSON(rw, v, err)
		return
	}
	post := map[string]func(map[string]any) (any, error){
		"/api/message":      w.apiMessage,
		"/api/command":      w.apiCommand,
		"/api/cancel":       func(map[string]any) (any, error) { return w.cancelTurn(), nil },
		"/api/model":        w.apiModel,
		"/api/permission":   w.apiPermission,
		"/api/history/open": w.apiOpen,
		"/api/regenerate":   w.apiRegenerate,
	}
	f, ok := post[r.URL.Path]
	if !ok {
		http.NotFound(rw, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(ct) != "application/json" {
		http.Error(rw, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<20)).Decode(&body); err != nil {
		http.Error(rw, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	v, err := f(body)
	writeJSON(rw, v, err)
}

// webError is an API error with its HTTP status.
type webError struct {
	code int
	msg  string
}

func (e *webError) Error() string { return e.msg }

func writeJSON(rw http.ResponseWriter, v any, err error) {
	rw.Header().Set("Content-Type", "application/json")
	if err != nil {
		code := http.StatusBadRequest
		var we *webError
		if errors.As(err, &we) {
			code = we.code
		}
		rw.WriteHeader(code)
		json.NewEncoder(rw).Encode(map[string]any{"error": err.Error()})
		return
	}
	if v == nil {
		v = map[string]any{"ok": true}
	}
	json.NewEncoder(rw).Encode(v)
}

func (w *webUI) serveEvents(rw http.ResponseWriter, r *http.Request) {
	fl, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan webEvent, 1024)
	w.subMu.Lock()
	if w.subsDone {
		w.subMu.Unlock()
		http.Error(rw, "shutting down", http.StatusServiceUnavailable)
		return
	}
	w.subs[ch] = struct{}{}
	w.subMu.Unlock()
	w.mu.Lock()
	pending := make([]map[string]any, 0, len(w.permReqs))
	for _, p := range w.permReqs {
		pending = append(pending, p)
	}
	w.mu.Unlock()
	defer func() {
		w.subMu.Lock()
		delete(w.subs, ch)
		w.subMu.Unlock()
	}()
	rw.Header().Set("Content-Type", "text/event-stream")
	send := func(e webEvent) {
		b, _ := json.Marshal(e.data)
		fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", e.kind, b)
		fl.Flush()
	}
	send(webEvent{"hello", map[string]any{"running": w.isRunning()}})
	for _, p := range pending {
		send(webEvent{"permission", p})
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			send(e)
		case <-ping.C:
			io.WriteString(rw, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (w *webUI) isRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// webCommands are the web UI's slash commands: chat's, plus /model and
// /history which chat does with flags.
var webCommands = []map[string]string{
	{"name": "/help", "desc": "show commands and keyboard shortcuts"},
	{"name": "/new", "alias": "/clear", "desc": "start a fresh conversation (new checkpoint)"},
	{"name": "/cost", "desc": "tokens and cost so far"},
	{"name": "/save", "desc": "save the checkpoint now, or to /save <path>"},
	{"name": "/compact", "desc": "compact the conversation before the next request"},
	{"name": "/raw", "desc": "toggle showing replies as raw Markdown"},
	{"name": "/model", "desc": "show or switch the model: /model <provider/model>"},
	{"name": "/history", "desc": "open a saved conversation"},
	{"name": "/stop", "desc": "interrupt the running turn (Esc)"},
	{"name": "/exit", "alias": "/quit", "desc": "stop the tinyagent web server"},
}

// webMessage is one conversation message as the UI shows it.
type webMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []model.ToolCall `json:"tool_calls,omitempty"`
	ToolID    string           `json:"tool_call_id,omitempty"`
	Images    int              `json:"images,omitempty"`
}

func webMessages(t *trajectory.Trajectory) []webMessage {
	if t == nil {
		return []webMessage{}
	}
	out := []webMessage{}
	for _, m := range t.Messages {
		if m.Role == "system" {
			continue
		}
		content := strings.ReplaceAll(m.Content, agent.SubmitMarker, "")
		if m.Role == "user" {
			// Show what was typed, not the follow-up preamble the agent adds.
			content = strings.TrimPrefix(content, trajectory.FollowUpNotice+"\n\n")
		}
		out = append(out, webMessage{Role: m.Role, Content: content, ToolCalls: m.ToolCalls, ToolID: m.ToolCallID, Images: len(m.Images)})
	}
	return out
}

// knownModels lists the model specs offered in the picker: the current
// one, the configured providers' models, and the built-in price table's
// models under the providers that serve them. Any provider/model may be
// typed.
func (w *webUI) knownModels() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(w.s.modelName)
	add(w.s.cfg.Model)
	for _, d := range model.ConfiguredProviders() {
		names := make([]string, 0, len(d.Models))
		for n := range d.Models {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			add(d.ID + "/" + n)
		}
	}
	for _, n := range model.KnownModels() {
		switch {
		case strings.HasPrefix(n, "claude"):
			add("anthropic/" + n)
		case strings.HasPrefix(n, "gpt") || strings.HasPrefix(n, "o"):
			add("openai/" + n)
		default:
			add("openrouter/" + n)
		}
	}
	return out
}

func (w *webUI) state() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, cfg := w.s, w.s.cfg
	t := s.traj
	st := map[string]any{
		"version":    currentVersion(),
		"cwd":        s.root,
		"model":      s.modelName,
		"handoff":    model.IsHandoff(s.modelName),
		"models":     w.knownModels(),
		"providers":  model.BuiltinProviders(),
		"running":    w.running,
		"turns":      w.turns,
		"checkpoint": relTo(s.root, w.path),
		"messages":   webMessages(t),
		"compact":    w.compact,
		"canRegen":   w.last != nil && !w.running,
		"markdown":   cfg.ChatMarkdown == nil || *cfg.ChatMarkdown,
		"settings": map[string]any{
			"sandbox":     cfg.Sandbox,
			"read_only":   cfg.ReadOnly,
			"lockdown":    s.lockdown != nil,
			"permissions": s.policy != nil,
			"ask_user":    cfg.AskUser.Enabled,
			"memory":      cfg.Memory.Enabled,
			"skills":      cfg.SkillsOn(),
			"streaming":   !s.o.noStream,
			"images":      true,
		},
	}
	if t != nil {
		st["usage"] = map[string]any{"steps": t.Steps, "input_tokens": t.InputTokens, "output_tokens": t.OutputTokens, "cost": t.Cost, "status": t.ExitStatus}
		if q := t.PendingQuestion; q != nil && t.ExitStatus == agent.StatusNeedsInput {
			st["question"] = map[string]any{"question": q.Question, "options": q.Options}
		}
	}
	agents := map[string]any{"enabled": !s.o.noAgentsMD}
	if !s.o.noAgentsMD {
		if b, err := os.ReadFile(filepath.Join(s.root, "AGENTS.md")); err == nil {
			txt := string(b)
			if len(txt) > 64<<10 {
				txt = txt[:64<<10] + "\n…"
			}
			agents["path"], agents["text"] = "AGENTS.md", txt
		}
	}
	st["agents_md"] = agents
	mem := map[string]any{"enabled": cfg.Memory.Enabled}
	if cfg.Memory.Enabled {
		if p, err := memoryStorePath(cfg.Memory, s.root); err == nil {
			mem["path"] = relTo(s.root, p)
			if es, err := memory.Load(p); err == nil {
				var list []map[string]any
				for _, e := range sortedEntries(es) {
					list = append(list, map[string]any{"id": e.ID, "status": e.Status, "text": e.Text, "created": e.CreatedAt.Format(time.RFC3339)})
				}
				mem["entries"] = list
			}
		}
	}
	st["memory"] = mem
	return st
}

// history lists the saved chat checkpoints in ./.tinyagent/chats, newest
// first.
func (w *webUI) history() (any, error) {
	dir := filepath.Join(w.s.root, ".tinyagent", "chats")
	ents, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	type item struct {
		Name     string  `json:"name"`
		Modified string  `json:"modified"`
		Task     string  `json:"task"`
		Status   string  `json:"status"`
		Model    string  `json:"model"`
		Cost     float64 `json:"cost"`
		Current  bool    `json:"current"`
		mod      time.Time
	}
	w.mu.Lock()
	cur := filepath.Base(w.path)
	w.mu.Unlock()
	out := []item{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		it := item{Name: e.Name(), mod: info.ModTime(), Modified: info.ModTime().Format(time.RFC3339), Current: e.Name() == cur}
		if t, err := trajectory.Load(filepath.Join(dir, e.Name())); err == nil {
			it.Task, it.Status, it.Model, it.Cost = firstLine(t.Task, 120), t.ExitStatus, t.Model, t.Cost
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	if len(out) > 200 {
		out = out[:200]
	}
	return out, nil
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

var errBusy = &webError{http.StatusConflict, "a turn is running; stop it first"}

// apiOpen continues a saved conversation, like chat --resume.
func (w *webUI) apiOpen(body map[string]any) (any, error) {
	name, _ := body["name"].(string)
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
		return nil, errors.New("name must be a checkpoint file name from the history list")
	}
	path := filepath.Join(w.s.root, ".tinyagent", "chats", name)
	t, err := trajectory.Load(path)
	if err != nil {
		return nil, err
	}
	if err := agent.ValidateResume(t, w.s.cfg, w.s.root); err != nil {
		return nil, err
	}
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil, errBusy
	}
	newPath, err := newChatPath(w.s.root)
	if err != nil {
		w.mu.Unlock()
		return nil, err
	}
	w.s.traj, w.source, w.cont, w.turns, w.compact, w.last = t, path, true, 0, false, nil
	w.s.remember = nil
	w.path = newPath
	w.mu.Unlock()
	if t.Model != "" && t.Model != w.s.modelName {
		if err := w.s.switchModel(t.Model, w.errw); err != nil {
			w.broadcast("notice", map[string]any{"text": "kept " + w.s.modelName + ": " + err.Error()})
		}
	}
	w.broadcast("state", nil)
	return map[string]any{"ok": true, "checkpoint": relTo(w.s.root, newPath)}, nil
}

func (w *webUI) apiModel(body map[string]any) (any, error) {
	spec, _ := body["model"].(string)
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("model is empty")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return nil, errBusy
	}
	if err := w.s.switchModel(spec, w.errw); err != nil {
		if h := model.Hint(spec, err.Error()); h != "" {
			err = fmt.Errorf("%v (%s)", err, h)
		}
		return nil, err
	}
	go w.broadcast("state", nil)
	return map[string]any{"ok": true, "model": spec}, nil
}

func (w *webUI) apiPermission(body map[string]any) (any, error) {
	id, _ := body["id"].(string)
	decision, _ := body["decision"].(string)
	reason, _ := body["reason"].(string)
	ans := permissions.Answer{Decision: permissions.DecisionDeny, Reason: reason, Channel: "web"}
	switch decision {
	case "allow":
		ans = permissions.Answer{Decision: permissions.DecisionAllow, Channel: "web"}
	case "always":
		ans = permissions.Answer{Decision: permissions.DecisionAllow, Always: true, Channel: "web"}
	case "deny":
	default:
		return nil, errors.New("decision must be allow, always or deny")
	}
	w.mu.Lock()
	ch, ok := w.perms[id]
	delete(w.perms, id)
	delete(w.permReqs, id)
	w.mu.Unlock()
	if !ok {
		return nil, &webError{http.StatusNotFound, "no pending permission request " + id}
	}
	ch <- ans
	w.broadcast("permission_done", map[string]any{"id": id, "decision": decision})
	return nil, nil
}

// approve answers a policy "ask" from the browser; with no answer before
// the turn ends it falls back to the policy's non-interactive decision.
func (w *webUI) approve(ctx context.Context, req permissions.Request) permissions.Answer {
	b := make([]byte, 8)
	rand.Read(b)
	id := hex.EncodeToString(b)
	ch := make(chan permissions.Answer, 1)
	ev := map[string]any{"id": id, "tool": req.Tool, "argument": req.Argument, "subagent": req.Subagent, "rule": req.Rule}
	w.mu.Lock()
	w.perms[id], w.permReqs[id] = ch, ev
	w.mu.Unlock()
	w.broadcast("permission", ev)
	select {
	case a := <-ch:
		return a
	case <-ctx.Done():
		w.mu.Lock()
		delete(w.perms, id)
		delete(w.permReqs, id)
		w.mu.Unlock()
		w.broadcast("permission_done", map[string]any{"id": id, "decision": "cancelled"})
		return permissions.Answer{}
	}
}

func (w *webUI) cancelTurn() any {
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return map[string]any{"ok": true, "cancelled": cancel != nil}
}

// webImages decodes and checks pasted or dropped images like --image.
func (w *webUI) webImages(body map[string]any) ([]model.Image, error) {
	raw, _ := body["images"].([]any)
	if len(raw) > model.MaxTaskImages {
		return nil, fmt.Errorf("%d images; at most %d may be attached", len(raw), model.MaxTaskImages)
	}
	maxBytes := w.s.cfg.MaxImageBytes
	if maxBytes <= 0 {
		maxBytes = tools.DefaultMaxImageBytes
	}
	var out []model.Image
	for i, r := range raw {
		m, _ := r.(map[string]any)
		label, _ := m["name"].(string)
		data, _ := m["data"].(string)
		if _, after, ok := strings.Cut(data, ","); ok && strings.HasPrefix(data, "data:") {
			data = after
		}
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("image %d (%s): bad base64", i+1, label)
		}
		if len(b) > maxBytes {
			return nil, fmt.Errorf("image %d (%s) is %s bytes; max_image_bytes is %s", i+1, label, model.FormatBytes(len(b)), model.FormatBytes(maxBytes))
		}
		mime := tools.SniffImage(b)
		if mime == "" {
			return nil, fmt.Errorf("image %d (%s) is not a PNG, JPEG, GIF, or WebP image", i+1, label)
		}
		if label == "" {
			label = fmt.Sprintf("image %d", i+1)
		}
		out = append(out, model.NewImage(mime, label, b))
	}
	return out, nil
}

func (w *webUI) apiMessage(body map[string]any) (any, error) {
	text, _ := body["text"].(string)
	images, err := w.webImages(body)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return nil, errors.New("message is empty")
	}
	if c := strings.TrimSpace(text); strings.HasPrefix(c, "/") && !strings.HasPrefix(c, "//") && len(images) == 0 {
		return w.apiCommand(map[string]any{"line": c})
	}
	if strings.HasPrefix(strings.TrimSpace(text), "//") {
		text = strings.Replace(text, "//", "/", 1)
	}
	return w.start(text, images, nil)
}

// apiRegenerate re-runs the latest message, or an edited version of it,
// from the conversation as it was before that turn. Files the turn changed
// stay changed.
func (w *webUI) apiRegenerate(body map[string]any) (any, error) {
	w.mu.Lock()
	snap := w.last
	w.mu.Unlock()
	if snap == nil {
		return nil, errors.New("nothing to regenerate: no turn has run in this session yet")
	}
	text, _ := body["text"].(string)
	if strings.TrimSpace(text) == "" {
		text = snap.msg
	}
	return w.start(text, nil, snap)
}

// apiCommand runs one slash command.
func (w *webUI) apiCommand(body map[string]any) (any, error) {
	line, _ := body["line"].(string)
	name, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	arg = strings.TrimSpace(arg)
	note := func(s string) (any, error) {
		w.broadcast("notice", map[string]any{"text": s})
		return map[string]any{"ok": true, "text": s}, nil
	}
	switch name {
	case "/help":
		return map[string]any{"ok": true, "help": true}, nil
	case "/exit", "/quit":
		go func() {
			time.Sleep(100 * time.Millisecond)
			p, _ := os.FindProcess(os.Getpid())
			p.Signal(os.Interrupt)
		}()
		return note("stopping tinyagent web; you can close this tab")
	case "/stop":
		return w.cancelTurn(), nil
	case "/model":
		if arg == "" {
			return note("model: " + w.s.modelName)
		}
		if _, err := w.apiModel(map[string]any{"model": arg}); err != nil {
			return nil, err
		}
		return note("model: " + arg)
	case "/history":
		return map[string]any{"ok": true, "history": true}, nil
	case "/raw":
		return map[string]any{"ok": true, "raw": true}, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return nil, errBusy
	}
	s := w.s
	switch name {
	case "/cost":
		t := s.traj
		if t == nil {
			return note("no turns yet")
		}
		txt := fmt.Sprintf("%s · %s in / %s out tokens", count(t.Steps, "step"), formatInt(t.InputTokens), formatInt(t.OutputTokens))
		if t.Cost > 0 {
			txt += " · " + formatCost(t.Cost)
		}
		return note(txt)
	case "/new", "/clear":
		s.traj, w.source, w.cont, w.turns, w.compact, w.last = nil, "", false, 0, false, nil
		s.remember = nil
		if s.o.output != "" {
			w.path = nextOutputPath(w.path)
		} else if p, err := newChatPath(s.root); err == nil {
			w.path = p
		} else {
			return nil, err
		}
		go w.broadcast("state", nil)
		return note("new conversation · checkpoint " + relTo(s.root, w.path))
	case "/save":
		if s.traj == nil {
			return note("nothing to save yet")
		}
		path := w.path
		if arg != "" {
			path = arg
			if !filepath.IsAbs(path) {
				path = filepath.Join(s.root, path)
			}
		}
		if err := s.traj.Save(path); err != nil {
			return nil, err
		}
		return note("saved " + relTo(s.root, path))
	case "/compact":
		if s.traj == nil {
			return note("nothing to compact yet")
		}
		if s.o.noCompact {
			return note("compaction is off (--no-compact)")
		}
		w.compact = true
		return note("the next message compacts the conversation first")
	}
	var names []string
	for _, c := range webCommands {
		names = append(names, strings.TrimPrefix(c["name"], "/"))
		if c["alias"] != "" {
			names = append(names, strings.TrimPrefix(c["alias"], "/"))
		}
	}
	msg := "unknown command " + name
	if sg := suggestFlags(strings.TrimPrefix(name, "/"), names); len(sg) > 0 {
		msg += "; did you mean /" + sg[0] + "?"
	}
	return nil, errors.New(msg + " (/help lists them; //text sends a message starting with /)")
}

// webWriter sends the agent's progress output to the browser as log events.
type webWriter struct{ w *webUI }

func (ww webWriter) Write(p []byte) (int, error) {
	if s := strings.TrimRight(string(p), "\n"); s != "" {
		ww.w.broadcast("log", map[string]any{"text": s})
	}
	return len(p), nil
}

// start begins a turn with msg in the background. from, when set, rewinds
// the conversation to before the latest turn first (edit / regenerate).
func (w *webUI) start(msg string, images []model.Image, from *webTurnSnap) (any, error) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil, errBusy
	}
	if w.closed {
		w.mu.Unlock()
		return nil, &webError{http.StatusServiceUnavailable, "shutting down"}
	}
	s := w.s
	if from != nil {
		s.traj, w.source, w.cont, w.turns, w.path = from.prev, from.source, from.cont, from.turns, from.path
		s.turn = from.turns
	}
	w.last = &webTurnSnap{prev: s.traj, source: w.source, cont: w.cont, msg: msg, turns: w.turns, path: w.path}
	s.turn++
	w.turns++
	prev := s.traj
	cfg := s.turnConfig(prev)
	var r turnResume
	if prev != nil {
		if prev.Model != s.modelName {
			// The model picker switched models mid-conversation: the
			// conversation carries on under the new one.
			prev.Model, prev.ActiveModel = s.modelName, ""
		}
		r = turnResume{prev: prev, source: w.source, cont: w.cont, followUp: msg}
	}
	a, err := s.turnAgent(cfg, images, r)
	if err != nil {
		s.turn--
		w.turns--
		w.mu.Unlock()
		return nil, fmt.Errorf("%v; nothing was sent", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.running, w.cancel, w.turnDone = true, cancel, make(chan struct{})
	a.CompactFirst, w.compact = w.compact, false
	turns, path, done := w.turns, w.path, w.turnDone
	w.mu.Unlock()
	go w.runTurn(ctx, cancel, a, cfg, msg, len(images), prev, path, turns, done, from != nil)
	return map[string]any{"ok": true, "turn": turns}, nil
}

func (w *webUI) runTurn(ctx context.Context, cancel context.CancelFunc, a *agent.Agent, cfg config.AgentConfig, msg string, nImages int, prev *trajectory.Trajectory, path string, turns int, done chan struct{}, rewind bool) {
	s := w.s
	defer close(done)
	defer cancel()
	a.Out, a.Err = webWriter{w}, webWriter{w}
	if s.policy != nil {
		a.PermissionApprover = w.approve
	}
	events := s.beginTurn(a, cfg, msg, prev, path, cancel, turns)
	defer events.close()
	streamed := false
	var smu sync.Mutex
	text := &markerFilter{emit: func(t string) {
		if t == "" {
			return
		}
		smu.Lock()
		streamed = true
		smu.Unlock()
		w.broadcast("delta", map[string]any{"text": t})
	}}
	var tmu sync.Mutex
	logged := a.OnEvent
	a.OnEvent = func(kind string, fields map[string]any) {
		if logged != nil {
			logged(kind, fields)
		}
		switch kind {
		case "tool.call":
			tmu.Lock()
			text.flush()
			tmu.Unlock()
			w.broadcast("tool_call", map[string]any{"id": fields["id"], "name": fields["name"], "arguments": fields["arguments"]})
		case "tool.output":
			w.broadcast("tool_output", map[string]any{"id": fields["id"], "name": fields["name"], "output": fields["output"]})
		case "tool.result":
			out, _ := fields["output"].(string)
			if len(out) > 200<<10 {
				out = out[:200<<10] + "\n… (truncated in the web UI)"
			}
			w.broadcast("tool_result", map[string]any{"id": fields["id"], "name": fields["name"], "output": out, "is_error": fields["is_error"]})
		case "compaction", "context.compacted":
			w.broadcast("notice", map[string]any{"text": "conversation compacted"})
		}
	}
	w.broadcast("turn_start", map[string]any{"turn": turns, "message": msg, "images": nImages, "model": s.modelName, "rewind": rewind})
	s.setText(func(t string) {
		tmu.Lock()
		defer tmu.Unlock()
		text.write(t)
	})
	start := time.Now()
	before := turnCounts(prev) // Run extends prev in place
	t := a.Run(ctx, msg)
	s.setText(nil)
	tmu.Lock()
	text.flush()
	tmu.Unlock()
	elapsed := time.Since(start)
	if err := t.Save(path); err != nil {
		w.broadcast("warning", map[string]any{"text": "checkpoint not saved: " + err.Error()})
	}
	end := map[string]any{"turn": turns, "status": t.ExitStatus, "submission": strings.ReplaceAll(t.Submission, agent.SubmitMarker, ""), "streamed": streamed, "line": turnSummary(turns, before, t, elapsed)}
	switch t.ExitStatus {
	case agent.StatusSubmitted, agent.StatusCompleted:
	case agent.StatusNeedsInput:
		if q := t.PendingQuestion; q != nil {
			end["question"] = map[string]any{"question": q.Question, "options": q.Options}
		}
	case agent.StatusError:
		end["error"] = t.Submission
		if h := model.Hint(s.modelName, t.Submission); h != "" {
			end["hint"] = h
		}
	}
	events.finish(t, elapsed)
	w.mu.Lock()
	s.traj, w.source, w.cont = t, "chat session", true
	w.running, w.cancel = false, nil
	w.mu.Unlock()
	w.broadcast("turn_end", end)
}
