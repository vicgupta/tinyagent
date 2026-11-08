package main

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

func fmtString(v any) string {
	s, _ := v.(string)
	return s
}

// wideContextConfig gives a 400,000-token window so a 1 MiB paste (~262k
// tokens) both fits and is large enough to trigger the context warning.
func wideContextConfig(t *testing.T) string {
	t.Helper()
	return writeConfig(t, "context_window: 400000\n")
}

func TestWebLargeMessage(t *testing.T) {
	p := newACPProvider(t)
	root := acpWorkspace(t)
	c := startWeb(t, root, "-c", wideContextConfig(t))
	ev := c.events()
	msg := strings.Repeat("w", 1<<20)
	r := c.post("/api/message", map[string]any{"text": msg})
	if r["ok"] != true || r["turn"] != float64(1) {
		t.Fatalf("message = %v", r)
	}
	es := until(t, ev, "turn_end")
	warns := kinds(es, "warning")
	if len(warns) == 0 || !strings.Contains(fmtString(warns[0]["text"]), "context window") {
		t.Errorf("expected context-window warning, got %v", warns)
	}
	start := kinds(es, "turn_start")
	if len(start) != 1 || start[0]["message"] != msg {
		t.Fatalf("turn_start message len = %d", len(fmtString(start[0]["message"])))
	}
	end := kinds(es, "turn_end")
	if len(end) != 1 || (end[0]["status"] != agent.StatusSubmitted && end[0]["status"] != agent.StatusCompleted) {
		t.Fatalf("turn_end = %v", end)
	}
	st := c.state()
	found := false
	for _, m := range st["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] == "user" && mm["content"] == msg {
			found = true
			break
		}
	}
	if !found {
		t.Error("1 MiB message not stored intact in /api/state")
	}
	reqs := p.requests()
	if len(reqs) == 0 {
		t.Fatal("provider received no request")
	}
	if got := lastUser(t, reqs[len(reqs)-1]); !strings.Contains(got, msg) {
		t.Errorf("provider user message len = %d, want to contain 1 MiB paste", len(got))
	}
}

func TestChatLargeCookedInput(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t)
	msg := strings.Repeat("c", 1<<20)
	r := runChatTest(t, root, strings.NewReader(msg+"\n/exit\n"), nil, "-m", "openai/gpt-4o", "-c", wideContextConfig(t))
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "warning:") || !strings.Contains(r.stderr, "context window") {
		t.Errorf("expected context-window warning:\n%s", r.stderr)
	}
	if strings.Contains(r.stderr, "context still exceeds the window after compaction") {
		t.Errorf("1 MiB paste should fit a 400,000-token window:\n%s", r.stderr)
	}
	reqs := p.requests()
	if len(reqs) == 0 {
		t.Fatalf("provider received no request; stderr:\n%s", r.stderr)
	}
	if got := lastUser(t, reqs[0]); !strings.Contains(got, msg) {
		t.Errorf("provider user message len = %d", len(got))
	}
}

func TestChatHugePasteKeepsContextGuard(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t)
	msg := strings.Repeat("g", 1<<20)
	r := runChatTest(t, root, strings.NewReader(msg+"\n/exit\n"), nil, "-m", "openai/gpt-4o")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "warning:") || !strings.Contains(r.stderr, "meets or exceeds") {
		t.Errorf("expected over-window warning:\n%s", r.stderr)
	}
	if !strings.Contains(r.stderr, "context still exceeds the window after compaction") {
		t.Errorf("context-window guard should still refuse a paste larger than the window:\n%s", r.stderr)
	}
	if n := len(p.requests()); n != 0 {
		t.Errorf("guard should not call the provider, got %d requests", n)
	}
}
