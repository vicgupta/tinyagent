package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/vicgupta/tinyagent/internal/trajectory"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// webTest is a tinyagent web session served by httptest.
type webTest struct {
	t    *testing.T
	w    *webUI
	srv  *httptest.Server
	host string
	root string
}

func startWeb(t *testing.T, root string, args ...string) *webTest {
	t.Helper()
	return startWebArgs(t, root, append([]string{"-m", "openai/gpt-4o"}, args...)...)
}

// startWebArgs is startWeb without the default -m, so the model comes from
// the environment, ./.env or the config.
func startWebArgs(t *testing.T, root string, args ...string) *webTest {
	t.Helper()
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse(append([]string{"--web", "--cwd", root, "--color", "never"}, args...)); err != nil {
		t.Fatal(err)
	}
	var errw bytes.Buffer
	s, prev, code := newChatSession(&o, fs, &errw)
	if code != 0 {
		t.Fatalf("newChatSession = %d: %s", code, errw.String())
	}
	w, err := newWebUI(s, prev, &errw)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(w)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	w.bind(port)
	t.Cleanup(func() {
		w.shutdown()
		srv.Close()
		s.Close()
	})
	return &webTest{t: t, w: w, srv: srv, host: "127.0.0.1:" + strconv.Itoa(port), root: root}
}

func (c *webTest) do(method, path string, body any, hdr map[string]string) (int, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-TinyAgent-Token", c.w.token)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (c *webTest) post(path string, body any) map[string]any {
	c.t.Helper()
	code, b := c.do("POST", path, body, nil)
	if code != 200 {
		c.t.Fatalf("POST %s = %d %s", path, code, b)
	}
	var v map[string]any
	json.Unmarshal(b, &v)
	return v
}

func (c *webTest) state() map[string]any {
	c.t.Helper()
	code, b := c.do("GET", "/api/state", nil, nil)
	if code != 200 {
		c.t.Fatalf("state = %d %s", code, b)
	}
	var v map[string]any
	json.Unmarshal(b, &v)
	return v
}

type sseEvent struct {
	kind string
	data map[string]any
}

// events subscribes to /api/events and returns the parsed events.
func (c *webTest) events() <-chan sseEvent {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.srv.URL+"/api/events?token="+url.QueryEscape(c.w.token), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		c.t.Fatalf("events = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ch := make(chan sseEvent, 1000)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 16<<20)
		var kind string
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event: "):
				kind = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: "):
				var d map[string]any
				json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &d)
				ch <- sseEvent{kind, d}
			}
		}
	}()
	if e := <-ch; e.kind != "hello" {
		c.t.Fatalf("first event = %v, want hello", e)
	}
	return ch
}

// until collects events up to and including the first of kind.
func until(t *testing.T, ch <-chan sseEvent, kind string) []sseEvent {
	t.Helper()
	var got []sseEvent
	timeout := time.After(20 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("event stream closed before %s; got %v", kind, got)
			}
			got = append(got, e)
			if e.kind == kind {
				return got
			}
		case <-timeout:
			t.Fatalf("no %s event; got %v", kind, got)
		}
	}
}

func kinds(es []sseEvent, kind string) []map[string]any {
	var out []map[string]any
	for _, e := range es {
		if e.kind == kind {
			out = append(out, e.data)
		}
	}
	return out
}

func TestWebRejectsMissingTokenBadHostAndOrigin(t *testing.T) {
	newACPProvider(t)
	c := startWeb(t, acpWorkspace(t))
	cases := []struct {
		name, method, path string
		hdr                map[string]string
		want               int
	}{
		{"no token", "GET", "/api/state", map[string]string{"X-TinyAgent-Token": ""}, 401},
		{"wrong token", "GET", "/api/state", map[string]string{"X-TinyAgent-Token": "nope"}, 401},
		{"no token page", "GET", "/", map[string]string{"X-TinyAgent-Token": ""}, 401},
		{"no token events", "GET", "/api/events", map[string]string{"X-TinyAgent-Token": ""}, 401},
		{"no token post", "POST", "/api/cancel", map[string]string{"X-TinyAgent-Token": ""}, 401},
		{"rebinding host", "GET", "/api/state", map[string]string{"Host": "evil.example:" + strings.Split(c.host, ":")[1]}, 403},
		{"other port host", "GET", "/api/state", map[string]string{"Host": "127.0.0.1:1"}, 403},
		{"rebinding host static", "GET", "/static/app.js", map[string]string{"Host": "attacker.test"}, 403},
		{"cross origin", "POST", "/api/cancel", map[string]string{"Origin": "http://evil.example"}, 403},
		{"ok", "GET", "/api/state", nil, 200},
		{"ok localhost", "GET", "/api/state", map[string]string{"Host": "localhost:" + strings.Split(c.host, ":")[1]}, 200},
		{"same origin", "POST", "/api/cancel", map[string]string{"Origin": "http://" + c.host}, 200},
		{"static needs no token", "GET", "/static/app.js", map[string]string{"X-TinyAgent-Token": ""}, 200},
		{"wrong method", "GET", "/api/message", nil, 405},
		{"not found", "GET", "/api/nope", nil, 404},
	}
	for _, tc := range cases {
		var body any
		if tc.method == "POST" {
			body = map[string]any{}
		}
		if code, b := c.do(tc.method, tc.path, body, tc.hdr); code != tc.want {
			t.Errorf("%s: %s %s = %d (%s), want %d", tc.name, tc.method, tc.path, code, b, tc.want)
		}
	}
	// Token in the query works (the URL tinyagent web prints).
	resp, err := http.Get(c.srv.URL + "/?token=" + url.QueryEscape(c.w.token))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "/static/app.js") {
		t.Errorf("page = %d %.200s", resp.StatusCode, b)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s header", h)
		}
	}
	// POST must be JSON (a form post from another page can't be).
	req, _ := http.NewRequest("POST", c.srv.URL+"/api/cancel", strings.NewReader("x=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-TinyAgent-Token", c.w.token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("form POST = %d, want 415", resp.StatusCode)
	}
}

func TestWebStateHasNoSecrets(t *testing.T) {
	newACPProvider(t)
	root := acpWorkspace(t)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET_THING=dotenv-secret-9911\n"), 0o600)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\nbe nice\n"), 0o644)
	c := startWeb(t, root)
	_, b := c.do("GET", "/api/state", nil, nil)
	if strings.Contains(string(b), "sk-acp-sentinel-4242") || strings.Contains(string(b), "dotenv-secret-9911") {
		t.Fatalf("state leaks a secret: %s", b)
	}
	st := c.state()
	if st["agents_md"].(map[string]any)["text"] != "# Rules\nbe nice\n" {
		t.Errorf("agents_md = %v", st["agents_md"])
	}
	if st["model"] != "openai/gpt-4o" || st["cwd"] != root {
		t.Errorf("state = %v %v", st["model"], st["cwd"])
	}
}

func TestWebMessageStreamsTextAndToolCalls(t *testing.T) {
	p := newACPProvider(t,
		acpReply{tool: "bash", args: `{"command":"echo hi-from-tool"}`},
		acpReply{text: "All **done**.\n" + agent.SubmitMarker},
	)
	root := acpWorkspace(t)
	c := startWeb(t, root)
	ev := c.events()
	r := c.post("/api/message", map[string]any{"text": "say hi"})
	if r["turn"] != float64(1) {
		t.Errorf("message = %v", r)
	}
	// A second message while the turn runs is refused.
	es := until(t, ev, "turn_end")
	start := kinds(es, "turn_start")
	if len(start) != 1 || start[0]["message"] != "say hi" {
		t.Errorf("turn_start = %v", start)
	}
	calls := kinds(es, "tool_call")
	if len(calls) != 1 || calls[0]["name"] != "bash" || !strings.Contains(fmtJSON(calls[0]["arguments"]), "hi-from-tool") {
		t.Fatalf("tool_call = %v", calls)
	}
	outputs := kinds(es, "tool_output")
	if len(outputs) != 1 || outputs[0]["id"] != calls[0]["id"] || outputs[0]["name"] != "bash" || outputs[0]["output"] != "hi-from-tool\n" {
		t.Errorf("tool_output = %v", outputs)
	}

	results := kinds(es, "tool_result")
	if len(results) != 1 || !strings.Contains(results[0]["output"].(string), "hi-from-tool") || results[0]["id"] != calls[0]["id"] {
		t.Errorf("tool_result = %v", results)
	}
	var text string
	for _, d := range kinds(es, "delta") {
		text += d["text"].(string)
	}
	if !strings.Contains(text, "All **done**.") || strings.Contains(text, agent.SubmitMarker) {
		t.Errorf("streamed text = %q", text)
	}
	end := kinds(es, "turn_end")[0]
	if end["status"] != agent.StatusSubmitted || end["streamed"] != true || !strings.Contains(end["line"].(string), "turn 1") {
		t.Errorf("turn_end = %v", end)
	}
	st := c.state()
	if st["turns"] != float64(1) || st["canRegen"] != true || st["running"] != false {
		t.Errorf("state = %v", st)
	}
	msgs := st["messages"].([]any)
	if len(msgs) < 4 || msgs[0].(map[string]any)["content"] != "say hi" {
		t.Errorf("messages = %v", msgs)
	}
	if _, err := os.Stat(filepath.Join(root, st["checkpoint"].(string))); err != nil {
		t.Errorf("checkpoint not saved: %v", err)
	}
	// The follow-up continues the same conversation.
	c.post("/api/message", map[string]any{"text": "and again"})
	if end := kinds(until(t, ev, "turn_end"), "turn_end")[0]; !strings.HasPrefix(end["line"].(string), "turn 2 · 1 step · ") {
		t.Errorf("follow-up turn line = %v", end["line"])
	}
	if got := lastUser(t, p.bodies[len(p.bodies)-1]); !strings.HasPrefix(got, trajectory.FollowUpNotice) || !strings.HasSuffix(got, "and again") {
		t.Errorf("follow-up sent %q", got)
	}
	found := false
	for _, m := range c.state()["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] == "user" && strings.Contains(mm["content"].(string), "and again") {
			found = mm["content"] == "and again"
			if !found {
				t.Errorf("state shows follow-up as %q; want the typed text only", mm["content"])
			}
		}
	}
	if !found {
		t.Error("follow-up missing from state")
	}
	if n := len(requestMessages(p.bodies[len(p.bodies)-1])); n < 5 {
		t.Errorf("follow-up request has %d messages; want the earlier turn too", n)
	}
}

func TestWebBusyAndCancel(t *testing.T) {
	p := newACPProvider(t)
	p.loop = &acpReply{tool: "bash", args: `{"command":"sleep 0.2"}`}
	c := startWeb(t, acpWorkspace(t))
	ev := c.events()
	c.post("/api/message", map[string]any{"text": "loop"})
	until(t, ev, "tool_call")
	if code, _ := c.do("POST", "/api/message", map[string]any{"text": "again"}, nil); code != 409 {
		t.Errorf("message while running = %d, want 409", code)
	}
	if code, _ := c.do("POST", "/api/model", map[string]any{"model": "openai/gpt-4o-mini"}, nil); code != 409 {
		t.Errorf("model switch while running = %d, want 409", code)
	}
	if r := c.post("/api/cancel", map[string]any{}); r["cancelled"] != true {
		t.Errorf("cancel = %v", r)
	}
	end := kinds(until(t, ev, "turn_end"), "turn_end")[0]
	if end["status"] != agent.StatusInterrupted {
		t.Errorf("status after cancel = %v", end["status"])
	}
}

func TestWebModelSwitch(t *testing.T) {
	p := newACPProvider(t)
	c := startWeb(t, acpWorkspace(t), "--fallback-model", "openai/gpt-4.1")
	ev := c.events()
	st := c.state()
	models := st["models"].([]any)
	if len(models) < 2 || models[0] != "openai/gpt-4o" {
		t.Errorf("models = %v", models[:min(5, len(models))])
	}
	if r := c.post("/api/command", map[string]any{"line": "/model"}); !strings.Contains(r["text"].(string), "openai/gpt-4o") || !strings.Contains(r["text"].(string), "openai/gpt-4.1") {
		t.Errorf("/model status = %v", r)
	}
	if r := c.post("/api/model", map[string]any{"model": "openai/gpt-4o-mini"}); r["model"] != "openai/gpt-4o-mini" {
		t.Errorf("switch = %v", r)
	}
	if st := c.state(); st["model"] != "openai/gpt-4o-mini" {
		t.Errorf("model after switch = %v", st["model"])
	}
	c.post("/api/message", map[string]any{"text": "hi"})
	until(t, ev, "turn_end")
	if m := p.bodies[len(p.bodies)-1]["model"]; m != "gpt-4o-mini" {
		t.Errorf("request model = %v, want gpt-4o-mini", m)
	}
	// /model <spec> does the same; a bad provider is an error and keeps
	// the current model.
	c.post("/api/command", map[string]any{"line": "/model openai/gpt-4o"})
	if st := c.state(); st["model"] != "openai/gpt-4o" {
		t.Errorf("model after /model = %v", st["model"])
	}
	// Mid-conversation: the next turn continues under the new model.
	c.post("/api/message", map[string]any{"text": "again"})
	end := kinds(until(t, ev, "turn_end"), "turn_end")[0]
	if end["status"] == "Error" || p.bodies[len(p.bodies)-1]["model"] != "gpt-4o" || len(requestMessages(p.bodies[len(p.bodies)-1])) < 4 {
		t.Errorf("turn after switch: %v, model %v", end, p.bodies[len(p.bodies)-1]["model"])
	}
	if code, b := c.do("POST", "/api/model", map[string]any{"model": "nosuchprovider/x"}, nil); code != 400 || !strings.Contains(string(b), "error") {
		t.Errorf("bad model = %d %s", code, b)
	}
	if st := c.state(); st["model"] != "openai/gpt-4o" {
		t.Errorf("model after bad switch = %v", st["model"])
	}
}

func TestWebSlashCommands(t *testing.T) {
	p := newACPProvider(t)
	root := acpWorkspace(t)
	c := startWeb(t, root)
	ev := c.events()
	if r := c.post("/api/command", map[string]any{"line": "/cost"}); r["text"] != "no turns yet" {
		t.Errorf("/cost = %v", r)
	}
	preview := c.post("/api/command", map[string]any{"line": "/context"})
	previewText, _ := preview["text"].(string)
	if !strings.Contains(previewText, "token window") || !strings.Contains(previewText, "estimated") {
		t.Errorf("/context before a turn = %v", preview)
	}
	c.post("/api/message", map[string]any{"text": "//etc/hosts is a path"})
	until(t, ev, "turn_end")
	if got := lastUser(t, p.bodies[0]); got != "/etc/hosts is a path" {
		t.Errorf("// sent %q", got)
	}
	if r := c.post("/api/message", map[string]any{"text": "/cost"}); !strings.Contains(r["text"].(string), "tokens") {
		t.Errorf("/cost via message = %v", r)
	}
	after := c.post("/api/command", map[string]any{"line": "/context"})
	afterText, _ := after["text"].(string)
	if !strings.Contains(afterText, "last turn") || !strings.Contains(afterText, "100 in") {
		t.Errorf("/context after a turn = %v", after)
	}
	if r := c.post("/api/command", map[string]any{"line": "/compact"}); !strings.Contains(r["text"].(string), "compacts") {
		t.Errorf("/compact = %v", r)
	}
	if st := c.state(); st["compact"] != true {
		t.Errorf("compact flag = %v", st["compact"])
	}
	saved := filepath.Join(root, "out.json")
	c.post("/api/command", map[string]any{"line": "/save out.json"})
	if _, err := os.Stat(saved); err != nil {
		t.Errorf("/save: %v", err)
	}
	for _, cmd := range []string{"/help", "/history", "/raw"} {
		if r := c.post("/api/command", map[string]any{"line": cmd}); r["ok"] != true {
			t.Errorf("%s = %v", cmd, r)
		}
	}
	before := c.state()["checkpoint"]
	c.post("/api/command", map[string]any{"line": "/clear"})
	st := c.state()
	if len(st["messages"].([]any)) != 0 || st["checkpoint"] == before || st["turns"] != float64(0) {
		t.Errorf("after /clear: %v", st)
	}
	code, b := c.do("POST", "/api/command", map[string]any{"line": "/cosst"}, nil)
	if code != 400 || !strings.Contains(string(b), "did you mean /cost") {
		t.Errorf("unknown command = %d %s", code, b)
	}
	_, b = c.do("GET", "/api/commands", nil, nil)
	for _, want := range []string{"/help", "/new", "/cost", "/context", "/save", "/compact", "/raw", "/model", "/history", "/stop", "/exit"} {
		if !strings.Contains(string(b), `"`+want+`"`) {
			t.Errorf("/api/commands lacks %s", want)
		}
	}
}

func TestWebPermissionPrompt(t *testing.T) {
	newACPProvider(t,
		acpReply{tool: "bash", args: `{"command":"echo allowed-run"}`},
		acpReply{tool: "bash", args: `{"command":"echo denied-run"}`},
		acpReply{text: "ok\n" + agent.SubmitMarker},
	)
	c := startWeb(t, acpWorkspace(t), "-c", writeConfig(t, "model: openai/gpt-4o\npermissions:\n  rules:\n    - tool: bash\n      command: \"echo *\"\n      decision: ask\n"))
	ev := c.events()
	c.post("/api/message", map[string]any{"text": "run things"})
	p1 := kinds(until(t, ev, "permission"), "permission")[0]
	if p1["tool"] != "bash" || !strings.Contains(p1["argument"].(string), "allowed-run") {
		t.Errorf("permission = %v", p1)
	}
	if st := c.state(); st["settings"].(map[string]any)["permissions"] != true {
		t.Errorf("settings = %v", st["settings"])
	}
	c.post("/api/permission", map[string]any{"id": p1["id"], "decision": "allow"})
	mid := until(t, ev, "permission")
	p2 := kinds(mid, "permission")[0]
	c.post("/api/permission", map[string]any{"id": p2["id"], "decision": "deny", "reason": "not that one"})
	es := append(mid, until(t, ev, "turn_end")...)
	var outs []string
	for _, r := range kinds(es, "tool_result") {
		outs = append(outs, r["output"].(string))
	}
	all := strings.Join(outs, "\n")
	if !strings.Contains(all, "allowed-run") || strings.Contains(all, "denied-run\n") || !strings.Contains(all, "not that one") {
		t.Errorf("tool results = %q", outs)
	}
	if code, _ := c.do("POST", "/api/permission", map[string]any{"id": "nope", "decision": "allow"}, nil); code != 404 {
		t.Errorf("unknown permission id = %d", code)
	}
	if code, _ := c.do("POST", "/api/permission", map[string]any{"id": "x", "decision": "maybe"}, nil); code != 400 {
		t.Errorf("bad decision = %d", code)
	}
}

func TestWebRegenerateAndEdit(t *testing.T) {
	p := newACPProvider(t)
	c := startWeb(t, acpWorkspace(t))
	ev := c.events()
	if code, _ := c.do("POST", "/api/regenerate", map[string]any{}, nil); code != 400 {
		t.Errorf("regenerate before a turn = %d", code)
	}
	c.post("/api/message", map[string]any{"text": "first"})
	until(t, ev, "turn_end")
	c.post("/api/message", map[string]any{"text": "second"})
	until(t, ev, "turn_end")
	c.post("/api/regenerate", map[string]any{"text": "second, edited"})
	start := kinds(until(t, ev, "turn_end"), "turn_start")[0]
	if start["rewind"] != true || start["message"] != "second, edited" {
		t.Errorf("turn_start = %v", start)
	}
	last := p.bodies[len(p.bodies)-1]
	if got := lastUser(t, last); !strings.HasSuffix(got, "\n\nsecond, edited") {
		t.Errorf("edited message sent %q", got)
	}
	b, _ := json.Marshal(requestMessages(last))
	if strings.Contains(string(b), `"second"`) || !strings.Contains(string(b), "first") {
		t.Errorf("edit should replace the latest turn only: %s", b)
	}
	c.post("/api/regenerate", map[string]any{})
	until(t, ev, "turn_end")
	if got := lastUser(t, p.bodies[len(p.bodies)-1]); !strings.HasSuffix(got, "\n\nsecond, edited") {
		t.Errorf("regenerate sent %q", got)
	}
	if st := c.state(); st["turns"] != float64(2) {
		t.Errorf("turns after regenerate = %v", st["turns"])
	}
}

func TestWebHistoryListAndOpen(t *testing.T) {
	p := newACPProvider(t)
	root := acpWorkspace(t)
	c := startWeb(t, root)
	ev := c.events()
	c.post("/api/message", map[string]any{"text": "remember the word pineapple"})
	until(t, ev, "turn_end")
	first := filepath.Base(c.state()["checkpoint"].(string))
	c.post("/api/command", map[string]any{"line": "/new"})
	time.Sleep(1100 * time.Millisecond) // checkpoint names have one-second resolution
	_, b := c.do("GET", "/api/history", nil, nil)
	var hist []map[string]any
	json.Unmarshal(b, &hist)
	if len(hist) != 1 || hist[0]["name"] != first || hist[0]["task"] != "remember the word pineapple" || hist[0]["status"] != agent.StatusSubmitted {
		t.Fatalf("history = %s", b)
	}
	for _, bad := range []string{"../x.json", "nope.json", ".hidden.json", "x.txt", ""} {
		if code, _ := c.do("POST", "/api/history/open", map[string]any{"name": bad}, nil); code == 200 {
			t.Errorf("open %q accepted", bad)
		}
	}
	r := c.post("/api/history/open", map[string]any{"name": first})
	if r["checkpoint"] == ".tinyagent/chats/"+first {
		t.Errorf("open should continue into a new checkpoint: %v", r)
	}
	st := c.state()
	if msgs := st["messages"].([]any); len(msgs) == 0 || msgs[0].(map[string]any)["content"] != "remember the word pineapple" {
		t.Errorf("opened messages = %v", msgs)
	}
	c.post("/api/message", map[string]any{"text": "what word?"})
	until(t, ev, "turn_end")
	b, _ = json.Marshal(requestMessages(p.bodies[len(p.bodies)-1]))
	if !strings.Contains(string(b), "pineapple") {
		t.Errorf("resumed request lacks the earlier turn: %s", b)
	}
}

// A 1x1 PNG.
const webTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func TestWebImages(t *testing.T) {
	p := newACPProvider(t)
	c := startWeb(t, acpWorkspace(t))
	ev := c.events()
	if code, b := c.do("POST", "/api/message", map[string]any{"text": "x", "images": []any{map[string]any{"name": "a.txt", "data": base64.StdEncoding.EncodeToString([]byte("hello"))}}}, nil); code != 400 || !strings.Contains(string(b), "not a PNG") {
		t.Errorf("bad image = %d %s", code, b)
	}
	c.post("/api/message", map[string]any{"text": "look", "images": []any{map[string]any{"name": "dot.png", "data": "data:image/png;base64," + webTestPNG}}})
	start := kinds(until(t, ev, "turn_end"), "turn_start")[0]
	if start["images"] != float64(1) {
		t.Errorf("turn_start images = %v", start["images"])
	}
	b, _ := json.Marshal(p.bodies[0])
	if !strings.Contains(string(b), "data:image/png;base64,") {
		t.Errorf("request has no image: %.300s", b)
	}
}

func TestListenWebPortFallback(t *testing.T) {
	old := webListen
	t.Cleanup(func() { webListen = old })
	var tried []string
	busy := map[string]bool{"127.0.0.1:7777": true, "127.0.0.1:7778": true}
	webListen = func(network, addr string) (net.Listener, error) {
		tried = append(tried, addr)
		if busy[addr] || busy["all"] && addr != "127.0.0.1:0" {
			return nil, errors.New("address already in use")
		}
		return old(network, "127.0.0.1:0")
	}
	ln, err := listenWeb(7777, false)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if strings.Join(tried, ",") != "127.0.0.1:7777,127.0.0.1:7778,127.0.0.1:7779" {
		t.Errorf("tried %v", tried)
	}
	tried = nil
	if _, err := listenWeb(7777, true); err == nil || !strings.Contains(err.Error(), "--port 7777") || len(tried) != 1 {
		t.Errorf("explicit busy port: err %v, tried %v", err, tried)
	}
	busy["all"], tried = true, nil
	ln, err = listenWeb(7777, false)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if len(tried) != 12 || tried[11] != "127.0.0.1:0" {
		t.Errorf("tried %v, want 7777-7787 then :0", tried)
	}
}

func TestListenWebRealBusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	if _, err := listenWeb(port, true); err == nil {
		t.Error("explicit busy port: no error")
	}
	ln, err := listenWeb(port, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if got := ln.Addr().(*net.TCPAddr); got.Port == port || !got.IP.IsLoopback() {
		t.Errorf("fallback listened on %v", got)
	}
}

func TestBrowserCommand(t *testing.T) {
	u := "http://127.0.0.1:7777/?token=abc"
	for _, tc := range []struct{ goos, name, args string }{
		{"darwin", "open", u},
		{"linux", "xdg-open", u},
		{"freebsd", "xdg-open", u},
		{"windows", "rundll32", "url.dll,FileProtocolHandler " + u},
	} {
		name, args := browserCommand(tc.goos, u)
		if name != tc.name || strings.Join(args, " ") != tc.args {
			t.Errorf("%s: %s %v", tc.goos, name, args)
		}
	}
	old := startBrowser
	t.Cleanup(func() { startBrowser = old })
	var got []string
	startBrowser = func(name string, args []string) error {
		got = append([]string{name}, args...)
		return errors.New("no display")
	}
	if err := openBrowser(u); err == nil || len(got) == 0 || got[len(got)-1] != u {
		t.Errorf("openBrowser: err %v, ran %v", err, got)
	}
}

func TestWebAssetsEmbedded(t *testing.T) {
	for _, f := range []string{"web/index.html", "web/app.js", "web/markdown.js", "web/style.css"} {
		b, err := webAssets.ReadFile(f)
		if err != nil || len(b) < 500 {
			t.Errorf("%s: %v (%d bytes)", f, err, len(b))
		}
	}
	idx, _ := webAssets.ReadFile("web/index.html")
	s := string(idx)
	for _, want := range []string{`src="/static/app.js"`, `src="/static/markdown.js"`, `href="/static/style.css"`, `lang="en"`} {
		if !strings.Contains(s, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	// The CSP forbids inline scripts, styles and handlers.
	if strings.Contains(s, "<script>") || strings.Contains(s, "<style") || strings.Contains(s, " onclick=") || strings.Contains(s, ` style="`) {
		t.Error("index.html has inline script or style")
	}
	app, _ := webAssets.ReadFile("web/app.js")
	for _, want := range []string{"X-TinyAgent-Token", "EventSource", "/api/regenerate", "/api/permission", "showModal", "prefers-color-scheme"} {
		if !strings.Contains(string(app)+s+mustAsset(t, "web/style.css"), want) {
			t.Errorf("UI lacks %q", want)
		}
	}
}

func fmtJSON(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func mustAsset(t *testing.T, name string) string {
	b, err := webAssets.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWebUIScripts runs the UI's JavaScript unit checks under node when it
// is installed (the markdown renderer's escaping and highlighting).
func TestWebUIScripts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	cmd := exec.Command(node, "web/ui_test.js")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("web/ui_test.js: %v\n%s", err, out)
	}
}

func TestWebFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--web", "--acp"}, "--web can't be combined with --acp"},
		{[]string{"--web", "-i"}, "--interactive can't be combined with --web"},
		{[]string{"--web-port", "8000"}, "apply only to tinyagent web"},
		{[]string{"--web-no-open"}, "apply only to tinyagent web"},
	} {
		code, _, out := runCLI(t, tc.args...)
		if code != 2 || !strings.Contains(out, tc.want) {
			t.Errorf("%v: code %d, out %q", tc.args, code, out)
		}
	}
	args, _, err := expandSubcommand([]string{"web", "--port", "8123", "--no-open", "-m", "openai/gpt-4o"})
	if err != nil || strings.Join(args, " ") != "--web --web-port 8123 --web-no-open --m openai/gpt-4o" {
		t.Errorf("expand = %v %v", args, err)
	}
	if args, sh, err := expandSubcommand([]string{"web"}); err != nil || sh != nil || strings.Join(args, " ") != "--web" {
		t.Errorf("bare web = %v %v %v", args, sh, err)
	}
	if _, sh, _ := expandSubcommand([]string{"web", "--help"}); sh == nil {
		t.Error("web --help shows no help")
	}
}

func TestWebToolOutputSuppression(t *testing.T) {
	for _, mode := range []string{"no-stream", "explicit-false"} {
		t.Run(mode, func(t *testing.T) {
			args := `{"command":"echo tool-output","stream":true}`
			var flags []string
			if mode == "no-stream" {
				flags = []string{"--no-stream"}
			} else {
				args = `{"command":"echo tool-output","stream":false}`
			}
			newACPProvider(t, acpReply{tool: "bash", args: args})
			c := startWeb(t, acpWorkspace(t), flags...)
			ev := c.events()
			c.post("/api/message", map[string]any{"text": "build"})
			es := until(t, ev, "turn_end")
			if len(kinds(es, "tool_output")) != 0 || len(kinds(es, "tool_result")) != 1 {
				t.Fatalf("events = %v", es)
			}
		})
	}
}
