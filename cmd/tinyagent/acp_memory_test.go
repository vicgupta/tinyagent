package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/memory"
)

// acpMemoryStore points the memory store at a temp state dir and returns
// the store path for root, seeded with an active and a pending entry.
func acpMemoryStore(t *testing.T, root string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	isolateMemoryConfig(t)
	store := testStorePath(t, root)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store,
		testEntry("act1", memory.StatusActive, "acp-active-note: run make test-fast", at),
		testEntry("pen1", memory.StatusPending, "acp-pending-note-should-not-load", at))
	return store
}

func rememberArgs(text string) string {
	b, _ := json.Marshal(map[string]string{"op": "add", "text": text})
	return string(b)
}

// systemPrompt returns a request body's system message text.
func systemPrompt(t *testing.T, body map[string]any) string {
	t.Helper()
	for _, m := range requestMessages(body) {
		if m["role"] == "system" {
			s, _ := m["content"].(string)
			return s
		}
	}
	t.Fatalf("no system message in %v", body)
	return ""
}

func loadStore(t *testing.T, store string) []memory.Entry {
	t.Helper()
	entries, err := memory.Load(store)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func entryWithText(entries []memory.Entry, text string) *memory.Entry {
	for i := range entries {
		if entries[i].Text == text {
			return &entries[i]
		}
	}
	return nil
}

func TestACPMemoryLoadsAndRemembers(t *testing.T) {
	root := acpWorkspace(t)
	store := acpMemoryStore(t, root)
	p := newACPProvider(t,
		acpReply{tool: "remember", args: rememberArgs("acp-new-note")},
		acpReply{text: "saved\n" + agent.SubmitMarker},
		acpReply{text: "second turn\n" + agent.SubmitMarker},
	)
	cfg := writeConfig(t, "model: openai/gpt-4o\nmemory:\n  enabled: true\n  approval: auto\n")
	c := startACP(t, "-c", cfg, "--no-stream")
	sess := c.newSession(root)
	if stop, _, updates := c.prompt(sess, "learn something"); stop != "end_turn" {
		t.Fatalf("turn 1 stop %q: %v", stop, updates)
	}
	if stop, _, updates := c.prompt(sess, "and again"); stop != "end_turn" {
		t.Fatalf("turn 2 stop %q: %v", stop, updates)
	}
	_, _, stderr := c.close()
	reqs := p.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d model requests, want 3", len(reqs))
	}
	first := systemPrompt(t, reqs[0])
	if !strings.Contains(first, "# Project memory") || !strings.Contains(first, "acp-active-note") {
		t.Errorf("turn 1 system prompt lacks the active entry:\n%s", first)
	}
	if strings.Contains(first, "acp-pending-note") {
		t.Error("pending entry loaded into the session")
	}
	for i, r := range reqs {
		if !slices.Contains(requestTools(r), "remember") {
			t.Errorf("request %d tools %v lack remember", i, requestTools(r))
		}
		if got := systemPrompt(t, r); got != first {
			t.Errorf("request %d system prompt changed within the session", i)
		}
		if strings.Contains(systemPrompt(t, r), "acp-new-note") {
			t.Errorf("request %d: the session sees its own write", i)
		}
	}
	e := entryWithText(loadStore(t, store), "acp-new-note")
	if e == nil || e.Status != memory.StatusActive || e.Source.UntrustedContext {
		t.Errorf("saved entry = %+v; stderr %s", e, stderr)
	}
}

func TestACPMemoryOffByDefault(t *testing.T) {
	root := acpWorkspace(t)
	acpMemoryStore(t, root)
	p := newACPProvider(t)
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\n"), "--no-stream")
	sess := c.newSession(root)
	c.prompt(sess, "hi")
	c.close()
	r := p.requests()[0]
	if slices.Contains(requestTools(r), "remember") || strings.Contains(systemPrompt(t, r), "acp-active-note") {
		t.Errorf("memory used with memory off: tools %v", requestTools(r))
	}
}

func TestACPMemoryFlagAndReadOnly(t *testing.T) {
	root := acpWorkspace(t)
	acpMemoryStore(t, root)
	cfg := writeConfig(t, "model: openai/gpt-4o\n")
	for _, c := range []struct {
		args     []string
		remember bool
	}{
		{[]string{"--memory"}, true},
		{[]string{"--memory", "--read-only"}, false},
	} {
		p := newACPProvider(t)
		conn := startACP(t, append([]string{"-c", cfg, "--no-stream"}, c.args...)...)
		sess := conn.newSession(root)
		conn.prompt(sess, "hi")
		conn.close()
		r := p.requests()[0]
		if !strings.Contains(systemPrompt(t, r), "acp-active-note") {
			t.Errorf("%v: active entry not loaded", c.args)
		}
		if got := slices.Contains(requestTools(r), "remember"); got != c.remember {
			t.Errorf("%v: remember offered = %v, want %v", c.args, got, c.remember)
		}
	}
}

func TestACPMemoryWriteLimitPerSession(t *testing.T) {
	root := acpWorkspace(t)
	store := acpMemoryStore(t, root)
	newACPProvider(t,
		acpReply{tool: "remember", args: rememberArgs("acp-limit-one")},
		acpReply{text: "ok\n" + agent.SubmitMarker},
		acpReply{tool: "remember", args: rememberArgs("acp-limit-two")},
		acpReply{text: "ok\n" + agent.SubmitMarker},
	)
	cfg := writeConfig(t, "model: openai/gpt-4o\nmemory:\n  enabled: true\n  approval: auto\n  max_writes_per_run: 1\n")
	c := startACP(t, "-c", cfg, "--no-stream")
	sess := c.newSession(root)
	c.prompt(sess, "one")
	_, _, updates := c.prompt(sess, "two")
	c.close()
	tu := toolUpdates(updates)
	if len(tu) != 3 || tu[2]["status"] != "failed" || !strings.Contains(toolResultText(tu[2]), "max_writes_per_run") {
		t.Errorf("second write in the session: %v", tu)
	}
	entries := loadStore(t, store)
	if entryWithText(entries, "acp-limit-one") == nil || entryWithText(entries, "acp-limit-two") != nil {
		t.Errorf("store = %+v", entries)
	}
}

func TestACPMemoryClientMCPServersForcePending(t *testing.T) {
	root := acpWorkspace(t)
	store := acpMemoryStore(t, root)
	newACPProvider(t,
		acpReply{tool: "remember", args: rememberArgs("acp-client-mcp-note")},
		acpReply{text: "ok\n" + agent.SubmitMarker},
	)
	cfg := writeConfig(t, "model: openai/gpt-4o\nacp:\n  client_mcp_servers: true\nmemory:\n  enabled: true\n  approval: auto\n")
	c := startACP(t, "-c", cfg, "--no-stream")
	server := []any{map[string]any{"name": "editor tools", "command": os.Args[0], "args": []string{"-test.run=^TestACPMCPFixtureProcess$"},
		"env": []any{map[string]any{"name": "TINYAGENT_ACP_MCP_FIXTURE", "value": "1"}}}}
	r, _ := c.wait(c.call("session/new", map[string]any{"cwd": root, "mcpServers": server}))
	res, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("session/new: %v", r)
	}
	c.prompt(res["sessionId"].(string), "remember")
	c.close()
	e := entryWithText(loadStore(t, store), "acp-client-mcp-note")
	if e == nil || e.Status != memory.StatusPending || !e.Source.UntrustedContext {
		t.Errorf("entry from a session with client MCP servers = %+v", e)
	}
}
