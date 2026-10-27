package main

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// acpPermissionPrompt runs one prompt turn, answering each
// session/request_permission with the given outcome, and returns the stop
// reason, the permission requests seen, and the tool updates.
func acpPermissionPrompt(t *testing.T, c *acpConn, sess, text, outcome string) (string, []map[string]any, []map[string]any) {
	t.Helper()
	id := c.call("session/prompt", textPrompt(sess, text))
	var updates, perms []map[string]any
	var r map[string]any
	for r == nil {
		m := c.next()
		switch {
		case m["method"] == "session/request_permission":
			perms = append(perms, m)
			var result map[string]any
			if outcome == "cancelled" {
				result = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
			} else {
				result = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": outcome}}
			}
			c.send(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": result})
		case m["method"] == "session/update":
			updates = append(updates, m["params"].(map[string]any)["update"].(map[string]any))
		default:
			if m["id"] == id {
				r = m
			}
		}
	}
	stop, _ := r["result"].(map[string]any)["stopReason"].(string)
	return stop, perms, updates
}

func TestACPPermissionAskReject(t *testing.T) {
	root := acpWorkspace(t)
	newACPProvider(t,
		acpReply{tool: "bash", args: `{"command":"curl https://x"}`},
		acpReply{text: "done\n" + agent.SubmitMarker},
	)
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\npermissions:\n  rules:\n    - tool: bash\n      command: \"curl *\"\n      decision: ask\n"))
	c.wait(c.call("initialize", map[string]any{"protocolVersion": 1}))
	sess := c.newSession(root)

	stop, perms, updates := acpPermissionPrompt(t, c, sess, "fetch it", "reject-once")
	if stop != "end_turn" {
		t.Fatalf("stop = %q, updates %v", stop, updates)
	}
	if len(perms) != 1 {
		t.Fatalf("permission requests = %v, want 1", perms)
	}
	req := perms[0]["params"].(map[string]any)
	if req["sessionId"] != sess {
		t.Errorf("sessionId = %v", req["sessionId"])
	}
	opts := req["options"].([]any)
	if len(opts) != 3 || opts[0].(map[string]any)["optionId"] != "allow-once" {
		t.Errorf("options = %v", opts)
	}
	// The rejected call reports a failed tool result.
	var failed bool
	for _, u := range toolUpdates(updates) {
		if u["sessionUpdate"] == "tool_call_update" && u["status"] == "failed" && strings.Contains(toolResultText(u), "denied by user") {
			failed = true
		}
	}
	if !failed {
		t.Errorf("no failed tool update with a deny: %v", toolUpdates(updates))
	}
	c.close()
}

func TestACPPermissionAskAllow(t *testing.T) {
	root := acpWorkspace(t)
	newACPProvider(t,
		acpReply{tool: "bash", args: `{"command":"echo allowed"}`},
		acpReply{text: "done\n" + agent.SubmitMarker},
	)
	c := startACP(t, "-c", writeConfig(t, "model: openai/gpt-4o\npermissions:\n  rules:\n    - tool: bash\n      command: \"echo *\"\n      decision: ask\n"))
	c.wait(c.call("initialize", map[string]any{"protocolVersion": 1}))
	sess := c.newSession(root)

	stop, perms, updates := acpPermissionPrompt(t, c, sess, "run it", "allow-once")
	if stop != "end_turn" {
		t.Fatalf("stop = %q, updates %v", stop, updates)
	}
	if len(perms) != 1 {
		t.Fatalf("permission requests = %v, want 1", perms)
	}
	var completed bool
	for _, u := range toolUpdates(updates) {
		if u["sessionUpdate"] == "tool_call_update" && u["status"] == "completed" && strings.Contains(toolResultText(u), "allowed") {
			completed = true
		}
	}
	if !completed {
		t.Errorf("allowed call did not complete with its output: %v", toolUpdates(updates))
	}
	c.close()
}
