package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// acpReply is one scripted model response: text, or one tool call.
type acpReply struct {
	text, tool, args string
}

// acpProvider is a fake OpenAI chat server answering with scripted replies
// (streamed when the request asks) and recording request bodies. After the
// replies run out it repeats loop, or submits.
type acpProvider struct {
	mu      sync.Mutex
	replies []acpReply
	loop    *acpReply
	tokens  int
	bodies  []map[string]any
}

func newACPProvider(t *testing.T, replies ...acpReply) *acpProvider {
	t.Helper()
	p := &acpProvider{replies: replies, tokens: 100}
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "sk-acp-sentinel-4242")
	t.Setenv("AGENT_MODEL", "")
	return p
}

func (p *acpProvider) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	p.bodies = append(p.bodies, body)
	reply := acpReply{text: "done\n" + agent.SubmitMarker}
	switch {
	case len(p.replies) > 0:
		reply, p.replies = p.replies[0], p.replies[1:]
	case p.loop != nil:
		reply = *p.loop
	}
	n, tokens := len(p.bodies), p.tokens
	p.mu.Unlock()
	usage := map[string]any{"prompt_tokens": tokens, "completion_tokens": 10}
	var calls []any
	if reply.tool != "" {
		calls = []any{map[string]any{"index": 0, "id": "call_" + strconv.Itoa(n), "type": "function", "function": map[string]any{"name": reply.tool, "arguments": reply.args}}}
	}
	if body["stream"] != true {
		msg := map[string]any{"role": "assistant", "content": reply.text}
		if calls != nil {
			msg["tool_calls"] = calls
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}}, "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	// Split the text so the submit marker straddles two chunks.
	mid := min(len(reply.text), len(reply.text)/2+3)
	for _, part := range []string{reply.text[:mid], reply.text[mid:]} {
		if part != "" {
			event(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}})
		}
	}
	if calls != nil {
		event(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": calls}}}})
	}
	event(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": usage})
	io.WriteString(w, "data: [DONE]\n\n")
}

func (p *acpProvider) requests() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.bodies...)
}

// requestMessages returns the chat messages of a request body.
func requestMessages(body map[string]any) []map[string]any {
	var out []map[string]any
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		out = append(out, m.(map[string]any))
	}
	return out
}

func requestTools(body map[string]any) []string {
	var names []string
	ts, _ := body["tools"].([]any)
	for _, t := range ts {
		names = append(names, t.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	return names
}

// acpConn drives runACP over pipes, like an editor. Every line it reads
// must be a JSON-RPC message; stderr is collected separately.
type acpConn struct {
	t      *testing.T
	in     *io.PipeWriter
	msgs   chan map[string]any
	code   chan int
	id     int
	stdout strings.Builder
	stderr chan string
	errW   *os.File
	oldErr *os.File
	mu     sync.Mutex
}

func startACP(t *testing.T, args ...string) *acpConn {
	t.Helper()
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse(append([]string{"--acp"}, args...)); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &acpConn{t: t, msgs: make(chan map[string]any, 1000), code: make(chan int, 1), stderr: make(chan string, 1), errW: w, oldErr: os.Stderr}
	go func() { b, _ := io.ReadAll(r); c.stderr <- string(b) }()
	os.Stderr = w
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c.in = inW
	go func() {
		code := runACP(&o, fs, inR, outW)
		outW.Close()
		c.code <- code
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 16<<20)
		for sc.Scan() {
			c.mu.Lock()
			c.stdout.WriteString(sc.Text() + "\n")
			c.mu.Unlock()
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m["jsonrpc"] != "2.0" {
				t.Errorf("stdout line is not a JSON-RPC message: %q", sc.Text())
				continue
			}
			c.msgs <- m
		}
		close(c.msgs)
	}()
	t.Cleanup(func() {
		if c.in != nil {
			c.close()
		}
	})
	return c
}

// close ends stdin and returns the exit code, stdout and stderr.
func (c *acpConn) close() (int, string, string) {
	c.t.Helper()
	c.in.Close()
	c.in = nil
	var code int
	select {
	case code = <-c.code:
	case <-time.After(20 * time.Second):
		c.t.Fatal("tinyagent --acp did not exit after stdin closed")
	}
	os.Stderr = c.oldErr
	c.errW.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	return code, c.stdout.String(), <-c.stderr
}

func (c *acpConn) send(v any) {
	b, _ := json.Marshal(v)
	c.in.Write(append(b, '\n'))
}

func (c *acpConn) call(method string, params any) float64 {
	c.id++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	return float64(c.id)
}

func (c *acpConn) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// next returns the next message, failing after a timeout.
func (c *acpConn) next() map[string]any {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatal("stdout closed")
		}
		return m
	case <-time.After(20 * time.Second):
		c.t.Fatal("timed out waiting for a message")
	}
	return nil
}

// wait collects session/update payloads until the response to id.
func (c *acpConn) wait(id float64) (map[string]any, []map[string]any) {
	c.t.Helper()
	var updates []map[string]any
	for {
		m := c.next()
		if m["method"] == "session/update" {
			updates = append(updates, m["params"].(map[string]any)["update"].(map[string]any))
			continue
		}
		if m["id"] == id {
			return m, updates
		}
	}
}

func (c *acpConn) newSession(cwd string) string {
	c.t.Helper()
	r, _ := c.wait(c.call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}))
	res, ok := r["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("session/new: %v", r)
	}
	return res["sessionId"].(string)
}

func textPrompt(sess, text string) map[string]any {
	return map[string]any{"sessionId": sess, "prompt": []any{map[string]any{"type": "text", "text": text}}}
}

// prompt runs one turn and returns its stop reason ("" on error), the
// response and the updates.
func (c *acpConn) prompt(sess, text string) (string, map[string]any, []map[string]any) {
	c.t.Helper()
	r, updates := c.wait(c.call("session/prompt", textPrompt(sess, text)))
	stop, _ := r["result"].(map[string]any)["stopReason"].(string)
	return stop, r, updates
}

func agentText(updates []map[string]any) string {
	var sb strings.Builder
	for _, u := range updates {
		if u["sessionUpdate"] == "agent_message_chunk" {
			sb.WriteString(u["content"].(map[string]any)["text"].(string))
		}
	}
	return sb.String()
}

// toolUpdates returns the tool_call and tool_call_update payloads.
func toolUpdates(updates []map[string]any) []map[string]any {
	var out []map[string]any
	for _, u := range updates {
		if k := u["sessionUpdate"]; k == "tool_call" || k == "tool_call_update" {
			out = append(out, u)
		}
	}
	return out
}

func toolResultText(u map[string]any) string {
	content, _ := u["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	return content[0].(map[string]any)["content"].(map[string]any)["text"].(string)
}

func acpWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(dir)
	return root
}

func TestACPInitialize(t *testing.T) {
	c := startACP(t, "-c", writeConfig(t, ""))
	r, _ := c.wait(c.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}))
	res := r["result"].(map[string]any)
	caps := res["agentCapabilities"].(map[string]any)
	pc := caps["promptCapabilities"].(map[string]any)
	info := res["agentInfo"].(map[string]any)
	if res["protocolVersion"] != 1.0 || caps["loadSession"] != false || pc["image"] != true || pc["embeddedContext"] != true || pc["audio"] != false || info["name"] != "tinyagent" || info["version"] != currentVersion() {
		t.Errorf("initialize = %v", res)
	}
	if mc := caps["mcpCapabilities"].(map[string]any); mc["http"] != false {
		t.Errorf("mcpCapabilities = %v", mc)
	}
	for _, line := range []string{"{oops", `{"jsonrpc":"2.0","id":99,"method":"terminal/create"}`} {
		c.in.Write([]byte(line + "\n"))
		if m := c.next(); m["error"] == nil {
			t.Errorf("%s: %v", line, m)
		}
	}
	if code, _, stderr := c.close(); code != 0 || !strings.Contains(stderr, "ACP mode") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestACPConflictingFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-t", "x"}, {"--task", "x"}, {"--resume", "r.json"}, {"--rewind", "r.json"}, {"--transcript", "r.json"},
		{"--review"}, {"--review-base", "main"}, {"--review-commit", "HEAD"}, {"--worktree"}, {"--output-schema", "s.json"},
		{"--json"}, {"--ask-user"}, {"--check"}, {"--init-config"}, {"--completion", "bash"}, {"-o", "x.json"}, {"--cwd", "."},
	} {
		code, _, stderr := runWith(t, append([]string{"--acp"}, args...)...)
		if code != 2 || !strings.Contains(stderr, "--acp") || !strings.Contains(stderr, args[0]) {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestACPSessionNewErrors(t *testing.T) {
	newACPProvider(t)
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"))
	missing := filepath.Join(t.TempDir(), "gone")
	for _, cwd := range []string{"relative/dir", missing} {
		r, _ := c.wait(c.call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}))
		e, _ := r["error"].(map[string]any)
		if e == nil || e["code"] != -32602.0 || !strings.Contains(e["message"].(string), cwd) {
			t.Errorf("cwd %s: %v", cwd, r)
		}
	}
	// No session was created.
	r, _ := c.wait(c.call("session/prompt", textPrompt("sess_x", "hi")))
	if r["error"] == nil {
		t.Errorf("prompt without a session: %v", r)
	}

	// A missing API key is reported with the headless hint.
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")
	r, _ = c.wait(c.call("session/new", map[string]any{"cwd": acpWorkspace(t), "mcpServers": []any{}}))
	e, _ := r["error"].(map[string]any)
	if e == nil || !strings.Contains(e["message"].(string), "hint: OPENAI_API_KEY isn't set") {
		t.Errorf("missing key: %v", r)
	}
	c.close()
}

func TestACPPromptTurn(t *testing.T) {
	root := acpWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha contents\n"), 0o644)
	p := newACPProvider(t,
		acpReply{text: "Let me look. ", tool: "read_file", args: `{"path":"a.txt"}`},
		acpReply{text: "It says alpha.\n" + agent.SubmitMarker},
		acpReply{text: "Still alpha.\n" + agent.SubmitMarker},
	)
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"))
	c.wait(c.call("initialize", map[string]any{"protocolVersion": 1}))
	sess := c.newSession(root)
	stop, _, updates := c.prompt(sess, "what is in a.txt?")
	if stop != "end_turn" {
		t.Fatalf("stop = %q, updates %v", stop, updates)
	}
	tu := toolUpdates(updates)
	if len(tu) != 3 {
		t.Fatalf("tool updates: %v", tu)
	}
	locs, _ := tu[0]["locations"].([]any)
	if tu[0]["sessionUpdate"] != "tool_call" || tu[0]["kind"] != "read" || tu[0]["status"] != "pending" || len(locs) != 1 || locs[0].(map[string]any)["path"] != filepath.Join(root, "a.txt") {
		t.Errorf("tool_call = %v", tu[0])
	}
	if tu[1]["status"] != "in_progress" || tu[2]["status"] != "completed" || !strings.Contains(toolResultText(tu[2]), "alpha contents") || tu[2]["toolCallId"] != tu[0]["toolCallId"] {
		t.Errorf("tool_call_updates = %v %v", tu[1], tu[2])
	}
	if got := agentText(updates); got != "Let me look. It says alpha.\n" {
		t.Errorf("agent text = %q", got)
	}
	// Text streamed before the call arrives before it.
	if updates[0]["sessionUpdate"] != "agent_message_chunk" {
		t.Errorf("first update = %v", updates[0])
	}
	if !slices.ContainsFunc(updates, func(u map[string]any) bool { return u["sessionUpdate"] == "usage_update" && u["size"].(float64) > 0 }) {
		t.Errorf("no usage_update: %v", updates)
	}

	// The second prompt continues the conversation as a follow-up.
	stop, _, _ = c.prompt(sess, "and now?")
	reqs := p.requests()
	if stop != "end_turn" || len(reqs) != 3 {
		t.Fatalf("second turn: %q, %d requests", stop, len(reqs))
	}
	first, second := requestMessages(reqs[1]), requestMessages(reqs[2])
	if len(second) != len(first)+2 || fmt.Sprint(second[:len(first)]) != fmt.Sprint(first) {
		t.Fatalf("history not continued:\n%v\n%v", first, second)
	}
	last := second[len(second)-1]["content"].(string)
	if !strings.HasPrefix(last, trajectory.FollowUpNotice) || !strings.HasSuffix(last, "and now?") {
		t.Errorf("follow-up message = %q", last)
	}
	code, stdout, stderr := c.close()
	if code != 0 || strings.Contains(stdout+stderr, "sk-acp-sentinel-4242") || strings.Contains(stdout, agent.SubmitMarker) {
		t.Errorf("exit %d; key or marker leaked:\n%s\n%s", code, stdout, stderr)
	}
}

func TestACPCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell and signals")
	}
	root := acpWorkspace(t)
	newACPProvider(t, acpReply{tool: "bash", args: `{"command":"echo $$ > pid; exec sleep 60"}`})
	// Every stdout line of initialize, new, prompt, cancel, prompt is a
	// JSON-RPC message (acpConn checks each).
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream", "-v")
	c.wait(c.call("initialize", map[string]any{"protocolVersion": 1}))
	sess := c.newSession(root)
	id := c.call("session/prompt", textPrompt(sess, "run it"))
	var pid int
	for deadline := time.Now().Add(10 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(filepath.Join(root, "pid"))
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatal("command did not start")
	}
	c.notify("session/cancel", map[string]any{"sessionId": sess})
	r, updates := c.wait(id)
	if r["error"] != nil || r["result"].(map[string]any)["stopReason"] != "cancelled" {
		t.Fatalf("cancelled prompt: %v", r)
	}
	if tu := toolUpdates(updates); len(tu) < 2 || tu[0]["kind"] != "execute" {
		t.Errorf("tool updates: %v", tu)
	}
	if proc, _ := os.FindProcess(pid); proc.Signal(syscall.Signal(0)) == nil {
		t.Errorf("command %d still running after cancel", pid)
	}
	if stop, _, _ := c.prompt(sess, "never mind"); stop != "end_turn" {
		t.Errorf("prompt after cancel: %q", stop)
	}
	if code, _, _ := c.close(); code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestACPLimitsPerTurn(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t)
	p.loop = &acpReply{tool: "list_files", args: `{}`}
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream", "--step-limit", "2")
	sess := c.newSession(root)
	for turn := 1; turn <= 2; turn++ {
		if stop, r, _ := c.prompt(sess, "keep going"); stop != "max_turn_requests" || len(p.requests()) != 2*turn {
			t.Fatalf("turn %d: %v after %d requests", turn, r, len(p.requests()))
		}
	}
	c.close()

	p = newACPProvider(t)
	p.loop, p.tokens = &acpReply{tool: "list_files", args: `{}`}, 1_000_000
	c = startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream", "--cost-limit", "0.5")
	sess = c.newSession(root)
	stop, _, updates := c.prompt(sess, "spend")
	if stop != "end_turn" || !strings.Contains(agentText(updates), "CostLimitExceeded") || len(p.requests()) != 1 {
		t.Errorf("cost limit: %q %q, %d requests", stop, agentText(updates), len(p.requests()))
	}
	c.close()
}

func TestACPReadOnly(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t, acpReply{tool: "bash", args: `{"command":"touch made"}`})
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream", "--read-only")
	sess := c.newSession(root)
	stop, _, updates := c.prompt(sess, "try to write")
	if got := requestTools(p.requests()[0]); stop != "end_turn" || fmt.Sprint(got) != "[read_file list_files grep]" {
		t.Errorf("stop %q, tools %v", stop, got)
	}
	if tu := toolUpdates(updates); len(tu) != 3 || tu[2]["status"] != "failed" {
		t.Errorf("bash in read-only: %v", tu)
	}
	if _, err := os.Stat(filepath.Join(root, "made")); err == nil {
		t.Error("read-only session ran a command")
	}
	c.close()
}

func TestACPFileAccessAndHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook is a shell script")
	}
	root := acpWorkspace(t)
	os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=hidden-value\n"), 0o644)
	hook := filepath.Join(t.TempDir(), "deny.sh")
	os.WriteFile(hook, []byte("#!/bin/sh\necho '{\"decision\":\"deny\",\"reason\":\"no listing today\"}'\n"), 0o755)
	cfg := writeConfig(t, "model: openai/gpt-4o\nfile_access:\n  deny_read: [\".env\"]\nhooks:\n  pre_tool:\n    - match: list_files\n      command: "+hook+"\n")
	newACPProvider(t, acpReply{tool: "read_file", args: `{"path":".env"}`}, acpReply{tool: "list_files", args: `{}`})
	c := startACP(t, "-c", cfg, "--no-stream")
	sess := c.newSession(root)
	_, _, updates := c.prompt(sess, "read secrets")
	tu := toolUpdates(updates)
	if len(tu) != 6 {
		t.Fatalf("tool updates: %v", tu)
	}
	if res := toolResultText(tu[2]); tu[2]["status"] != "failed" || strings.Contains(res, "hidden-value") {
		t.Errorf("deny_read: %v", tu[2])
	}
	if res := toolResultText(tu[5]); tu[5]["status"] != "failed" || !strings.Contains(res, "denied by pre_tool hook") || !strings.Contains(res, "no listing today") {
		t.Errorf("pre_tool hook: %v", tu[5])
	}
	c.close()
}

func TestACPSandbox(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no sandbox backend on " + runtime.GOOS)
	}
	root := acpWorkspace(t)
	outside := filepath.Join(t.TempDir(), "escaped")
	newACPProvider(t, acpReply{tool: "bash", args: fmt.Sprintf(`{"command":"echo x > %s; echo y > inside"}`, outside)})
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream", "--sandbox")
	r, _ := c.wait(c.call("session/new", map[string]any{"cwd": root, "mcpServers": []any{}}))
	res, ok := r["result"].(map[string]any)
	if !ok {
		c.close()
		t.Skipf("sandbox unavailable: %v", r["error"])
	}
	c.prompt(res["sessionId"].(string), "escape")
	if _, err := os.Stat(outside); err == nil {
		t.Error("sandboxed command wrote outside the session's cwd")
	}
	if _, err := os.Stat(filepath.Join(root, "inside")); err != nil {
		t.Errorf("sandboxed command could not write inside cwd: %v", err)
	}
	c.close()
}

// TestACPMCPFixtureProcess is the MCP server a client supplies in
// TestACPClientMCPServers.
func TestACPMCPFixtureProcess(t *testing.T) {
	if os.Getenv("TINYAGENT_ACP_MCP_FIXTURE") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong from the editor's server"}}}, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestACPClientMCPServers(t *testing.T) {
	root := acpWorkspace(t)
	marker := filepath.Join(t.TempDir(), "started")
	ignored := []any{map[string]any{"name": "editor tools", "command": "/bin/sh", "args": []string{"-c", "touch " + marker}, "env": []any{}}}

	// Off by default: nothing starts, and the first turn says so.
	newACPProvider(t)
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream")
	r, _ := c.wait(c.call("session/new", map[string]any{"cwd": root, "mcpServers": ignored}))
	sess := r["result"].(map[string]any)["sessionId"].(string)
	_, _, updates := c.prompt(sess, "hi")
	if text := agentText(updates); !strings.Contains(text, "editor tools") || !strings.Contains(text, "acp.client_mcp_servers") {
		t.Errorf("first message: %q", text)
	}
	c.close()
	if _, err := os.Stat(marker); err == nil {
		t.Error("an ignored client MCP server was started")
	}

	// With the opt-in, the server starts and its tools are callable.
	p := newACPProvider(t, acpReply{tool: "editor_tools__ping", args: `{}`})
	c = startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\nacp:\n  client_mcp_servers: true\n"), "--no-stream")
	server := []any{map[string]any{"name": "editor tools", "command": os.Args[0], "args": []string{"-test.run=^TestACPMCPFixtureProcess$"},
		"env": []any{map[string]any{"name": "TINYAGENT_ACP_MCP_FIXTURE", "value": "1"}}}}
	r, _ = c.wait(c.call("session/new", map[string]any{"cwd": root, "mcpServers": server}))
	res, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("session/new: %v", r)
	}
	_, _, updates = c.prompt(res["sessionId"].(string), "ping")
	if !slices.Contains(requestTools(p.requests()[0]), "editor_tools__ping") {
		t.Errorf("tools: %v", requestTools(p.requests()[0]))
	}
	if tu := toolUpdates(updates); len(tu) != 3 || tu[2]["status"] != "completed" || !strings.Contains(toolResultText(tu[2]), "pong") {
		t.Errorf("mcp call: %v", tu)
	}
	c.close()
}

func TestACPImagePrompt(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t)
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream")
	sess := c.newSession(root)
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))
	r, _ := c.wait(c.call("session/prompt", map[string]any{"sessionId": sess, "prompt": []any{
		map[string]any{"type": "text", "text": "what is this?"},
		map[string]any{"type": "image", "mimeType": "image/png", "data": png},
	}}))
	b, _ := json.Marshal(p.requests())
	if r["error"] != nil || !strings.Contains(string(b), "data:image/png;base64,") {
		t.Errorf("image prompt: %v\n%s", r, b)
	}
	for _, block := range []map[string]any{
		{"type": "audio", "mimeType": "audio/wav", "data": "AAAA"},
		{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString([]byte("not an image"))},
	} {
		r, _ = c.wait(c.call("session/prompt", map[string]any{"sessionId": sess, "prompt": []any{map[string]any{"type": "text", "text": "x"}, block}}))
		if e, _ := r["error"].(map[string]any); e == nil || e["code"] != -32602.0 {
			t.Errorf("%v: %v", block["type"], r)
		}
	}
	c.close()
}

func TestMarkerFilter(t *testing.T) {
	var got strings.Builder
	f := &markerFilter{emit: func(s string) { got.WriteString(s) }}
	m := agent.SubmitMarker
	for _, part := range []string{"done ", m[:5], m[5:20], m[20:] + " tail", " COMPLETE_"} {
		f.write(part)
	}
	f.flush()
	if got.String() != "done  tail COMPLETE_" {
		t.Errorf("filtered = %q", got.String())
	}
}

func TestACPLogDB(t *testing.T) {
	root := acpWorkspace(t)
	newACPProvider(t)
	path := filepath.Join(t.TempDir(), "log.db")
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream", "--log-db="+path)
	sess := c.newSession(root)
	c.prompt(sess, "one")
	c.prompt(sess, "two")
	c.close()
	db, err := sql.Open("sqlite", eventlog.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var runs, turns int
	db.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
	db.QueryRow(`SELECT count(*) FROM events WHERE type = 'acp.turn' AND payload LIKE ?`, "%"+sess+"%").Scan(&turns)
	if runs != 2 || turns != 2 {
		t.Errorf("runs %d, acp.turn events %d", runs, turns)
	}
}
