package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

func cutStep(calls ...model.ToolCall) model.Message {
	m := model.Message{Role: "assistant", Content: "partial", ToolCalls: calls,
		Extra: map[string]any{model.ExtraTruncated: true, model.ExtraTruncationReason: "max_tokens"}}
	return m
}

func TestTruncatedCallsNotRun(t *testing.T) {
	var kinds []string
	var root string
	tr, m := runSteps(t, func(a *Agent) {
		root = a.Tools.Root
		a.OnEvent = func(k string, f map[string]any) { kinds = append(kinds, k) }
	}, cutStep(call("1", "write_file", `{"path":"f","content":"x"}`), call("2", "write_file", `{"path":"g","con`)))
	if _, err := os.Stat(filepath.Join(root, "f")); err == nil {
		t.Error("call from truncated response ran")
	}
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.Contains(res[0], "cut off at the output-token limit") {
		t.Fatalf("results = %q", res)
	}
	if tr.FormatErrors != 0 || tr.Truncations != 1 || tr.ConsecutiveTruncations != 0 || tr.ExitStatus != StatusCompleted {
		t.Errorf("format %d, trunc %d/%d, status %s", tr.FormatErrors, tr.Truncations, tr.ConsecutiveTruncations, tr.ExitStatus)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "model.truncated") || strings.Contains(joined, "tool.call") {
		t.Errorf("events = %s", joined)
	}
	last := m.seen[len(m.seen)-1]
	if u := last[len(last)-1]; u.Role != "user" {
		t.Errorf("no notice appended: %+v", u)
	}
}

func TestTruncationCap(t *testing.T) {
	tr, _ := runSteps(t, nil, cutStep(), cutStep(), cutStep())
	if tr.ExitStatus != StatusOutputTruncated || tr.Truncations != 3 {
		t.Errorf("status %s, truncations %d", tr.ExitStatus, tr.Truncations)
	}
	tr, _ = runSteps(t, func(a *Agent) { a.Config.MaxTruncations = 1 }, cutStep())
	if tr.ExitStatus != StatusOutputTruncated {
		t.Errorf("cap 1: status %s", tr.ExitStatus)
	}
}

func TestTruncationResets(t *testing.T) {
	ok := toolStep(call("1", "list_files", `{}`))
	tr, _ := runSteps(t, nil, cutStep(), cutStep(), ok, cutStep(), cutStep())
	if tr.ExitStatus != StatusCompleted || tr.Truncations != 4 {
		t.Errorf("status %s, truncations %d", tr.ExitStatus, tr.Truncations)
	}
}

func TestTruncatedSubmitIgnored(t *testing.T) {
	s := cutStep()
	s.Content = "x\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"
	tr, _ := runSteps(t, nil, s)
	if tr.ExitStatus != StatusCompleted || tr.Truncations != 1 {
		t.Errorf("status %s", tr.ExitStatus)
	}
}
