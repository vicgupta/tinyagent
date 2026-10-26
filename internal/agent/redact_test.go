package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

const secret = "s3cr3tvalue123"

// redactingAgent offers one MCP-style tool that prints secret, with
// redact_secrets on.
func redactingAgent(t *testing.T, m model.Model) *Agent {
	a := newAgent(t, m)
	a.Tools.ExternalSpecs = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "srv__env", Parameters: map[string]any{"type": "object"}}}}
	a.Tools.ExternalCall = func(_ context.Context, name, _ string) (string, bool, bool) {
		return "FOO_TOKEN=" + secret, false, name == "srv__env"
	}
	a.Tools.Redactor = eventlog.NewValueRedactor([]string{secret}, false)
	a.RedactSecrets = &trajectory.RedactSecretsInfo{Enabled: true, Names: []string{"FOO_TOKEN"}}
	return a
}

func TestRedactSecretsToolResultHookAndEvents(t *testing.T) { // AC2, AC7, AC10
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "srv__env", `{}`)}},
		{Role: "assistant", Content: "done\n" + SubmitMarker},
	}}
	a := redactingAgent(t, m)
	stdin := filepath.Join(t.TempDir(), "post.json")
	withHooks(t, a, config.Hooks{PostTool: []config.Hook{
		hookstest.Hook(t, "", "stdin", stdin),
		hookstest.Hook(t, "", "exit2", "hook saw "+secret),
	}})
	var events []string
	var result map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		b, _ := json.Marshal(f)
		events = append(events, kind+" "+string(b))
		if kind == "tool.result" {
			result = f
		}
	}
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s", tr.ExitStatus)
	}
	sent := m.seen[1][len(m.seen[1])-1].Content
	if strings.Contains(sent, secret) || !strings.HasPrefix(sent, "FOO_TOKEN=[REDACTED]") || !strings.Contains(sent, "hook saw [REDACTED]") {
		t.Errorf("provider got %q", sent)
	}
	if result["redactions"] != 2 {
		t.Errorf("tool.result redactions = %v", result["redactions"])
	}
	in, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(in), secret) || !strings.Contains(string(in), "FOO_TOKEN=[REDACTED]") {
		t.Errorf("post_tool hook input = %s", in)
	}
	b, _ := json.Marshal(tr)
	if strings.Contains(string(b), secret) || strings.Contains(strings.Join(events, "\n"), secret) {
		t.Error("secret in trajectory or events")
	}
	if tr.RedactSecrets == nil || tr.RedactSecrets.Names[0] != "FOO_TOKEN" {
		t.Errorf("trajectory redact_secrets = %+v", tr.RedactSecrets)
	}
}

func TestRedactSecretsStopHookReason(t *testing.T) { // AC7
	m := &scripted{replies: []model.Message{
		{Role: "assistant", Content: "first\n" + SubmitMarker},
		{Role: "assistant", Content: "second\n" + SubmitMarker},
	}}
	a := redactingAgent(t, m)
	h, _ := gateHook(t, "1", "deploy failed with key "+secret)
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s", tr.ExitStatus)
	}
	last := m.seen[1][len(m.seen[1])-1].Content
	if strings.Contains(last, secret) || !strings.Contains(last, "deploy failed with key [REDACTED]") {
		t.Errorf("stop reason sent as %q", last)
	}
}

func TestRedactSecretsResumeKeepsEarlierMessages(t *testing.T) { // AC12
	printCmd := `{"command":"printf 'FOO_TOKEN=%s' ` + secret + `"}`
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", printCmd)}},
		{Role: "assistant", Content: "x"},
	}}
	a := newAgent(t, m)
	a.Config.StepLimit = 1
	first := a.Run(context.Background(), "task")
	if first.ExitStatus != StatusStepLimit {
		t.Fatalf("status %s", first.ExitStatus)
	}
	if got := toolResults(first.Messages); len(got) != 1 || !strings.Contains(got[0], "FOO_TOKEN="+secret) {
		t.Fatalf("redaction off changed the result: %q", got)
	}

	m2 := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("2", "bash", printCmd)}},
		{Role: "assistant", Content: "done\n" + SubmitMarker},
	}}
	b := newAgent(t, m2)
	b.Tools.Root = a.Tools.Root
	b.Tools.Redactor = eventlog.NewValueRedactor([]string{secret}, false)
	b.Resume = first
	tr := b.Run(context.Background(), "")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s: %s", tr.ExitStatus, tr.Submission)
	}
	got := toolResults(tr.Messages)
	if len(got) != 2 || !strings.Contains(got[0], "FOO_TOKEN="+secret) || strings.Contains(got[1], secret) || !strings.Contains(got[1], "FOO_TOKEN=[REDACTED]") {
		t.Errorf("tool results after resume = %q", got)
	}
}
