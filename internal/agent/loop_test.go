package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// scripted replays canned responses and records what it was sent.
type scripted struct {
	replies []model.Message
	seen    [][]model.Message
}

func (s *scripted) Query(_ context.Context, msgs []model.Message, _ []model.ToolSpec) (model.Message, model.Cost, error) {
	s.seen = append(s.seen, append([]model.Message(nil), msgs...))
	if len(s.replies) == 0 {
		return model.Message{}, model.Cost{}, errors.New("out of replies")
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, model.Cost{USD: 0.01, InputTokens: 100, OutputTokens: 20}, nil
}

func call(id, name, args string) model.ToolCall {
	return model.ToolCall{ID: id, Type: "function", Function: model.FunctionCall{Name: name, Arguments: args}}
}

func newAgent(t *testing.T, m model.Model) *Agent {
	return &Agent{Model: m, ModelName: "fake", Tools: &tools.Executor{Root: t.TempDir()}, Config: config.Default()}
}

func TestRunSubmits(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
		{Role: "assistant", Content: "Done.\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}
	a := newAgent(t, m)
	out := filepath.Join(t.TempDir(), "traj.json")
	saves := 0
	a.OnStep = func(tr *trajectory.Trajectory) {
		saves++
		if err := tr.Save(out); err != nil {
			t.Fatal(err)
		}
	}
	tr := a.Run(context.Background(), "write hi")

	if tr.ExitStatus != StatusSubmitted || tr.Submission != "Done." || tr.Steps != 2 {
		t.Errorf("trajectory = %+v", tr)
	}
	if b, _ := os.ReadFile(filepath.Join(a.Tools.Root, "f.txt")); string(b) != "hi" {
		t.Errorf("file = %q", b)
	}
	// system, user, assistant(tool call), tool result were sent on the second query.
	second := m.seen[1]
	if len(second) != 4 || second[3].Role != "tool" || second[3].ToolCallID != "1" || second[3].Content != "ok" {
		t.Errorf("second query history = %+v", second)
	}
	if tr.InputTokens != 200 || tr.OutputTokens != 40 {
		t.Errorf("tokens = %d in / %d out, want 200 / 40", tr.InputTokens, tr.OutputTokens)
	}
	if saves != 2 {
		t.Errorf("saved %d times, want 2", saves)
	}
	if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}
}

func TestRunCompletesWithoutToolCalls(t *testing.T) {
	a := newAgent(t, &scripted{replies: []model.Message{{Role: "assistant", Content: "answer"}}})
	tr := a.Run(context.Background(), "q")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "answer" {
		t.Errorf("trajectory = %+v", tr)
	}
}

func TestLimits(t *testing.T) {
	loop := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "list_files", `{}`)}}
	replies := func() []model.Message { return []model.Message{loop, loop, loop, loop, loop} }

	a := newAgent(t, &scripted{replies: replies()})
	a.Config.StepLimit = 2
	if tr := a.Run(context.Background(), "x"); tr.ExitStatus != StatusStepLimit || tr.Steps != 2 {
		t.Errorf("step limit: %s after %d", tr.ExitStatus, tr.Steps)
	}

	a = newAgent(t, &scripted{replies: replies()})
	a.Config.CostLimit = 0.025
	if tr := a.Run(context.Background(), "x"); tr.ExitStatus != StatusCostLimit || tr.Steps != 3 {
		t.Errorf("cost limit: %s after %d", tr.ExitStatus, tr.Steps)
	}

	a = newAgent(t, &scripted{replies: replies()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tr := a.Run(ctx, "x"); tr.ExitStatus != StatusInterrupted || tr.Steps != 0 {
		t.Errorf("cancelled: %s after %d", tr.ExitStatus, tr.Steps)
	}
}

func TestModelErrorEndsRun(t *testing.T) {
	a := newAgent(t, &scripted{})
	if tr := a.Run(context.Background(), "x"); tr.ExitStatus != StatusError || tr.Submission != "out of replies" {
		t.Errorf("trajectory = %+v", tr)
	}
}

func TestStreamedTextNotReprinted(t *testing.T) {
	var out strings.Builder
	a := newAgent(t, &scripted{replies: []model.Message{{Role: "assistant", Content: "answer"}}})
	a.Out, a.Streamed = &out, true
	a.Run(context.Background(), "q")
	if out.String() != "\n" {
		t.Errorf("output = %q, want only the line ending", out.String())
	}
}

func firstSystem(t *testing.T, a *Agent) string {
	t.Helper()
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: "answer"}}}
	a.Model = m
	a.Run(context.Background(), "q")
	return m.seen[0][0].Content
}

func TestAgentsMD(t *testing.T) {
	a := newAgent(t, nil)
	base := firstSystem(t, a)
	if strings.Contains(base, "AGENTS.md") {
		t.Fatalf("prompt mentions AGENTS.md without the file: %q", base)
	}

	if err := os.WriteFile(filepath.Join(a.Tools.Root, "AGENTS.md"), []byte("run make check"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	a.Out, a.Verbose = &out, true
	if got, want := firstSystem(t, a), base+"\n\n## Project instructions (AGENTS.md)\n\nrun make check"; got != want {
		t.Errorf("system = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "loaded AGENTS.md (14 bytes)\n") {
		t.Errorf("verbose output = %q", out.String())
	}

	a.SkipAgentsMD = true
	if got := firstSystem(t, a); got != base {
		t.Errorf("SkipAgentsMD: system = %q, want %q", got, base)
	}
}

func TestAgentsMDTruncated(t *testing.T) {
	a := newAgent(t, nil)
	big := strings.Repeat("x", maxAgentsMD+100)
	if err := os.WriteFile(filepath.Join(a.Tools.Root, "AGENTS.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := firstSystem(t, a)
	if !strings.HasSuffix(got, big[:maxAgentsMD]+"\n[AGENTS.md truncated]") || strings.Contains(got, big[:maxAgentsMD+1]) {
		t.Errorf("system not truncated with marker; tail = %q", got[len(got)-40:])
	}
}

func TestAgentsMDReadErrorWarnsOnErr(t *testing.T) {
	a := newAgent(t, nil)
	base := firstSystem(t, a)
	if err := os.Mkdir(filepath.Join(a.Tools.Root, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	a.Out, a.Err = &out, &errOut
	if got := firstSystem(t, a); got != base {
		t.Errorf("system = %q, want %q", got, base)
	}
	if strings.Contains(out.String(), "AGENTS.md") {
		t.Errorf("warning on Out: %q", out.String())
	}
	if !strings.HasPrefix(errOut.String(), "warning: reading AGENTS.md:") {
		t.Errorf("Err = %q, want the warning", errOut.String())
	}
}

func TestAgentsMDTruncatedOnRuneBoundary(t *testing.T) {
	a := newAgent(t, nil)
	// "é" is 2 bytes; the odd prefix puts a rune across the cap.
	big := "x" + strings.Repeat("é", maxAgentsMD)
	if err := os.WriteFile(filepath.Join(a.Tools.Root, "AGENTS.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := firstSystem(t, a)
	if !utf8.ValidString(got) {
		t.Errorf("system is not valid UTF-8; tail = %q", got[len(got)-40:])
	}
	body, ok := strings.CutSuffix(got, "\n[AGENTS.md truncated]")
	if !ok {
		t.Fatalf("no truncation marker; tail = %q", got[len(got)-40:])
	}
	_, md, _ := strings.Cut(body, "## Project instructions (AGENTS.md)\n\n")
	if len(md) > maxAgentsMD || len(md) < maxAgentsMD-utf8.UTFMax {
		t.Errorf("kept %d bytes, want at most %d", len(md), maxAgentsMD)
	}
}
