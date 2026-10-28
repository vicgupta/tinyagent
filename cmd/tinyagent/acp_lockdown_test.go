package main

import (
	"os"
	"strings"
	"testing"
)

func TestACPUntrustedTaskRequiresLockdown(t *testing.T) {
	_, requests := lockdownEnv(t)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--acp", "--untrusted-task")
	})
	if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, "--untrusted-task requires lockdown") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// acpFixtureServer is the client-supplied MCP server of
// TestACPMCPFixtureProcess, as session/new passes it.
func acpFixtureServer() []any {
	return []any{map[string]any{"name": "editor tools", "command": os.Args[0], "args": []string{"-test.run=^TestACPMCPFixtureProcess$"},
		"env": []any{map[string]any{"name": "TINYAGENT_ACP_MCP_FIXTURE", "value": "1"}}}}
}

func (c *acpConn) newSessionWith(cwd string, servers []any) string {
	c.t.Helper()
	r, _ := c.wait(c.call("session/new", map[string]any{"cwd": cwd, "mcpServers": servers}))
	res, ok := r["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("session/new: %v", r)
	}
	return res["sessionId"].(string)
}

// TestACPLockdownClientMCP checks that lockdown works in an ACP session: a
// client-supplied MCP server's result taints the session as
// acp_client_mcp, and the taint carries into the next prompt, which can't
// call that server again.
func TestACPLockdownClientMCP(t *testing.T) {
	root := acpWorkspace(t)
	newACPProvider(t,
		acpReply{tool: "editor_tools__ping", args: `{}`}, acpReply{text: "ok"},
		acpReply{tool: "editor_tools__ping", args: `{}`}, acpReply{text: "ok"})
	cfg := writeConfig(t, "model: openai/gpt-4o\nacp:\n  client_mcp_servers: true\nuntrusted_content:\n  enabled: true\n")
	c := startACP(t, "-c", cfg, "--no-stream")
	sess := c.newSessionWith(root, acpFixtureServer())
	_, _, updates := c.prompt(sess, "ping")
	if tu := toolUpdates(updates); len(tu) < 3 || tu[2]["status"] != "completed" || !strings.Contains(toolResultText(tu[2]), "pong") {
		t.Fatalf("first call: %v", tu)
	}
	_, _, updates = c.prompt(sess, "ping again")
	tu := toolUpdates(updates)
	if len(tu) < 3 || strings.Contains(toolResultText(tu[2]), "pong") || !strings.Contains(toolResultText(tu[2]), "acp_client_mcp") {
		t.Errorf("call after taint: %v", tu)
	}
	code, _, stderr := c.close()
	if code != 0 || !strings.Contains(stderr, "lockdown") || !strings.Contains(stderr, "acp_client_mcp") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// TestACPLockdownUntrustedTask checks that --untrusted-task taints an ACP
// session from its first prompt.
func TestACPLockdownUntrustedTask(t *testing.T) {
	root := acpWorkspace(t)
	newACPProvider(t, acpReply{tool: "editor_tools__ping", args: `{}`}, acpReply{text: "ok"})
	cfg := writeConfig(t, "model: openai/gpt-4o\nacp:\n  client_mcp_servers: true\n")
	c := startACP(t, "-c", cfg, "--no-stream", "--lockdown", "--untrusted-task")
	sess := c.newSessionWith(root, acpFixtureServer())
	_, _, updates := c.prompt(sess, "ping")
	tu := toolUpdates(updates)
	if len(tu) < 3 || strings.Contains(toolResultText(tu[2]), "pong") || !strings.Contains(toolResultText(tu[2]), "untrusted_task") {
		t.Errorf("call in a tainted session: %v", tu)
	}
	code, _, stderr := c.close()
	if code != 0 || !strings.Contains(stderr, "session starts tainted (untrusted_task)") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}
