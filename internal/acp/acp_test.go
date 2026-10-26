package acp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeSession blocks each turn until release (or cancellation) and records
// prompts.
type fakeSession struct {
	prompts []Prompt
	started chan struct{}
	release chan struct{}
	closed  bool
}

func (f *fakeSession) Prompt(ctx context.Context, p Prompt, u *Updates) (string, error) {
	f.prompts = append(f.prompts, p)
	u.Message("working")
	f.started <- struct{}{}
	select {
	case <-f.release:
		return StopEndTurn, nil
	case <-ctx.Done():
		return "", errors.New("interrupted")
	}
}

func (f *fakeSession) Close() { f.closed = true }

type client struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan map[string]any
	done  chan error
}

func serve(t *testing.T, newSession NewSessionFunc) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, lines: make(chan map[string]any, 100), done: make(chan error, 1)}
	s := &Server{Name: "tinyagent", Version: "v1.2.3", NewSession: newSession}
	go func() { c.done <- s.Serve(context.Background(), inR, outW); outW.Close() }()
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m["jsonrpc"] != "2.0" {
				t.Errorf("not a JSON-RPC message: %q", sc.Text())
			}
			c.lines <- m
		}
		close(c.lines)
	}()
	return c
}

func (c *client) send(line string) { c.in.Write([]byte(line + "\n")) }

// next returns the next message from the server.
func (c *client) next() map[string]any {
	c.t.Helper()
	select {
	case m := <-c.lines:
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("no message from the server")
	}
	return nil
}

// response skips notifications up to the response with id.
func (c *client) response(id float64) map[string]any {
	c.t.Helper()
	for {
		m := c.next()
		if m["id"] == id {
			return m
		}
	}
}

func errCode(m map[string]any) float64 {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(float64)
	return code
}

func TestInitialize(t *testing.T) {
	c := serve(t, nil)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}`)
	r := c.next()["result"].(map[string]any)
	caps := r["agentCapabilities"].(map[string]any)
	pc := caps["promptCapabilities"].(map[string]any)
	if r["protocolVersion"] != 1.0 || caps["loadSession"] != false || pc["image"] != true || pc["embeddedContext"] != true || pc["audio"] != false {
		t.Errorf("initialize = %v", r)
	}
	if info := r["agentInfo"].(map[string]any); info["name"] != "tinyagent" || info["version"] != "v1.2.3" {
		t.Errorf("agentInfo = %v", info)
	}
	// A newer client gets version 1; an older one an error.
	c.send(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":7}}`)
	if r := c.next(); r["result"].(map[string]any)["protocolVersion"] != 1.0 {
		t.Errorf("newer client: %v", r)
	}
	c.send(`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":0}}`)
	if r := c.next(); errCode(r) != CodeInvalidParams {
		t.Errorf("older client: %v", r)
	}
	c.in.Close()
	if err := <-c.done; err != nil {
		t.Fatal(err)
	}
}

func TestMalformedInput(t *testing.T) {
	c := serve(t, nil)
	for _, tc := range []struct {
		line string
		code float64
	}{
		{`{not json`, CodeParseError},
		{`[1,2]`, CodeInvalidRequest},
		{`{"jsonrpc":"1.0","id":4,"method":"initialize"}`, CodeInvalidRequest},
		{`{"jsonrpc":"2.0","id":5,"method":"fs/frobnicate"}`, CodeMethodNotFound},
		{`{"jsonrpc":"2.0","id":6,"method":"session/prompt","params":{"sessionId":"nope","prompt":[{"type":"text","text":"x"}]}}`, CodeInvalidParams},
		{`{"jsonrpc":"2.0","id":7,"method":"session/new","params":{"cwd":"relative/dir","mcpServers":[]}}`, CodeInvalidParams},
	} {
		c.send(tc.line)
		if r := c.next(); errCode(r) != tc.code {
			t.Errorf("%s: got %v, want code %v", tc.line, r, tc.code)
		}
	}
	// Unknown notifications are ignored; the server still answers.
	c.send(`{"jsonrpc":"2.0","method":"$/unknown"}`)
	c.send(`{"jsonrpc":"2.0","id":8,"method":"authenticate","params":{"methodId":"x"}}`)
	if r := c.next(); r["id"] != 8.0 || r["error"] != nil {
		t.Errorf("authenticate: %v", r)
	}
	c.in.Close()
	if err := <-c.done; err != nil {
		t.Fatal(err)
	}
}

func TestPromptTurns(t *testing.T) {
	f := &fakeSession{started: make(chan struct{}, 1), release: make(chan struct{})}
	var gotCwd string
	c := serve(t, func(_ context.Context, id, cwd string, _ []MCPServer) (Session, error) {
		gotCwd = cwd
		return f, nil
	})
	c.send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/work","mcpServers":[]}}`)
	id, _ := c.next()["result"].(map[string]any)["sessionId"].(string)
	if !strings.HasPrefix(id, "sess_") || gotCwd != "/work" {
		t.Fatalf("session %q cwd %q", id, gotCwd)
	}
	prompt := func(n int) {
		c.send(`{"jsonrpc":"2.0","id":` + string(rune('0'+n)) + `,"method":"session/prompt","params":{"sessionId":"` + id + `","prompt":[{"type":"text","text":"hi"}]}}`)
	}

	// A second prompt while one runs is refused; the first completes.
	prompt(2)
	<-f.started
	if u := c.next(); u["method"] != "session/update" {
		t.Fatalf("update: %v", u)
	}
	prompt(3)
	if r := c.next(); r["id"] != 3.0 || errCode(r) != CodeInvalidRequest {
		t.Fatalf("concurrent prompt: %v", r)
	}
	f.release <- struct{}{}
	if r := c.response(2); r["result"].(map[string]any)["stopReason"] != StopEndTurn {
		t.Fatalf("first prompt: %v", r)
	}

	// session/cancel answers the turn with cancelled, not an error.
	prompt(4)
	<-f.started
	c.send(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"` + id + `"}}`)
	if r := c.response(4); r["error"] != nil || r["result"].(map[string]any)["stopReason"] != StopCancelled {
		t.Fatalf("cancelled prompt: %v", r)
	}

	// The session still works; closing stdin cancels a running turn and
	// closes the session.
	prompt(5)
	<-f.started
	c.in.Close()
	if r := c.response(5); r["result"].(map[string]any)["stopReason"] != StopCancelled {
		t.Fatalf("prompt at EOF: %v", r)
	}
	if err := <-c.done; err != nil || !f.closed {
		t.Fatalf("serve: %v, closed %v", err, f.closed)
	}
}

func TestParsePrompt(t *testing.T) {
	text := "func main() {}\n"
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nxxxx"))
	p, err := ParsePrompt([]ContentBlock{
		{Type: "text", Text: "explain this"},
		{Type: "resource", Resource: &Resource{URI: "file:///w/main.go", Text: &text}},
		{Type: "resource_link", URI: "file:///w/go.mod", Name: "go.mod"},
		{Type: "image", MimeType: "image/png", Data: png, URI: "file:///w/shot.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "explain this\n\nContext from file:///w/main.go:\n```\nfunc main() {}\n```\n\nReference: go.mod (file:///w/go.mod)"
	if p.Text != want {
		t.Errorf("text = %q", p.Text)
	}
	if len(p.Images) != 1 || p.Images[0].Label != "shot.png" || !strings.HasPrefix(string(p.Images[0].Data), "\x89PNG") {
		t.Errorf("images = %+v", p.Images)
	}
	for _, bad := range [][]ContentBlock{
		{{Type: "audio", Data: "AAAA"}},
		{{Type: "text", Text: "x"}, {Type: "resource", Resource: &Resource{URI: "file:///a.bin", Blob: "AAAA"}}},
		{{Type: "text", Text: "x"}, {Type: "image", Data: "%%%"}},
		{{Type: "image", Data: png}},
		{},
	} {
		var e *Error
		if _, err := ParsePrompt(bad); !errors.As(err, &e) || e.Code != CodeInvalidParams {
			t.Errorf("%+v: err = %v", bad, err)
		}
	}
	// Fences grow past backtick runs inside the resource.
	ticks := "a ``` b"
	p, _ = ParsePrompt([]ContentBlock{{Type: "resource", Resource: &Resource{URI: "u", Text: &ticks}}})
	if !strings.Contains(p.Text, "````\na ``` b\n````") {
		t.Errorf("fence: %q", p.Text)
	}
}
