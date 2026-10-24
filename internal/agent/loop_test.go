package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// newAgent returns an Agent rooted at a fresh temp dir. The dir gets a .git so
// the AGENTS.md lookup stops there and never reads files above it.
func newAgent(t *testing.T, m model.Model) *Agent {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Agent{Model: m, ModelName: "fake", Tools: &tools.Executor{Root: root}, Config: config.Default()}
}

func TestRunSubmits(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
		{Role: "assistant", Content: "Done.\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}
	a := newAgent(t, m)
	out := filepath.Join(t.TempDir(), "traj.json")
	saves := 0
	a.OnStep = func(tr *trajectory.Trajectory) error {
		saves++
		if err := tr.Save(out); err != nil {
			t.Fatal(err)
		}
		return nil
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

func TestReadOnlyRunFiltersAndDenies(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("denied", "write_file", `{"path":"x","content":"bad"}`), call("allowed", "list_files", `{}`)}},
		{Role: "assistant", Content: "Review complete"},
	}}
	a := newAgent(t, m)
	a.Tools.ReadOnly = true
	tr := a.Run(context.Background(), "review")
	if !tr.ReadOnly {
		t.Fatal("trajectory did not record read-only mode")
	}
	if len(m.seen) != 2 || len(m.seen[0]) == 0 {
		t.Fatalf("requests=%d", len(m.seen))
	}
	if len(m.seen[1]) < 5 || m.seen[1][3].ToolCallID != "denied" || !strings.Contains(m.seen[1][3].Content, "read-only mode") || m.seen[1][4].ToolCallID != "allowed" {
		t.Fatalf("tool results=%+v", m.seen[1])
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "x")); !os.IsNotExist(err) {
		t.Fatalf("write happened: %v", err)
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
	if want := fmt.Sprintf("loaded AGENTS.md from %s (14 bytes)\n", filepath.Join(a.Tools.Root, "AGENTS.md")); !strings.Contains(out.String(), want) {
		t.Errorf("verbose output = %q, want %q", out.String(), want)
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

// writeTestFile creates parent directories and writes content to path.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nestedRepo returns a repository root with a .git directory and a nested
// working directory two levels below it.
func nestedRepo(t *testing.T) (repo, cwd string) {
	t.Helper()
	repo = t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd = filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, cwd
}

func agentAt(t *testing.T, root string) *Agent {
	t.Helper()
	a := newAgent(t, nil)
	a.Tools.Root = root
	return a
}

func TestAgentsMDFromAncestor(t *testing.T) {
	repo, cwd := nestedRepo(t)
	a := agentAt(t, cwd)
	base := firstSystem(t, a)
	writeTestFile(t, filepath.Join(repo, "AGENTS.md"), "repo rules")

	var out strings.Builder
	a.Out, a.Verbose = &out, true
	if got, want := firstSystem(t, a), base+"\n\n## Project instructions (AGENTS.md)\n\nrepo rules"; got != want {
		t.Errorf("system = %q, want %q", got, want)
	}
	if want := fmt.Sprintf("loaded AGENTS.md from %s (10 bytes)\n", filepath.Join(repo, "AGENTS.md")); !strings.Contains(out.String(), want) {
		t.Errorf("verbose output = %q, want %q", out.String(), want)
	}

	a.SkipAgentsMD = true
	if got := firstSystem(t, a); got != base {
		t.Errorf("SkipAgentsMD: system = %q, want %q", got, base)
	}
}

func TestAgentsMDInCwdWinsOverAncestor(t *testing.T) {
	repo, cwd := nestedRepo(t)
	writeTestFile(t, filepath.Join(repo, "AGENTS.md"), "repo rules")
	writeTestFile(t, filepath.Join(cwd, "AGENTS.md"), "cwd rules")
	got := firstSystem(t, agentAt(t, cwd))
	if !strings.HasSuffix(got, "## Project instructions (AGENTS.md)\n\ncwd rules") || strings.Contains(got, "repo rules") {
		t.Errorf("system = %q, want the cwd file only", got)
	}
}

func TestAgentsMDNearestAncestorWins(t *testing.T) {
	repo, cwd := nestedRepo(t)
	writeTestFile(t, filepath.Join(repo, "AGENTS.md"), "root rules")
	writeTestFile(t, filepath.Join(repo, "services", "AGENTS.md"), "services rules")
	got := firstSystem(t, agentAt(t, cwd))
	if !strings.HasSuffix(got, "## Project instructions (AGENTS.md)\n\nservices rules") || strings.Contains(got, "root rules") {
		t.Errorf("system = %q, want the closest ancestor file only", got)
	}
}

func TestAgentsMDNotReadAboveRepoRoot(t *testing.T) {
	outer := t.TempDir()
	repo := filepath.Join(outer, "repo")
	cwd := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := agentAt(t, cwd)
	base := firstSystem(t, a)

	writeTestFile(t, filepath.Join(outer, "AGENTS.md"), "outside rules")
	var errOut strings.Builder
	a.Err = &errOut
	if got := firstSystem(t, a); got != base {
		t.Errorf("system = %q, want %q", got, base)
	}
	if errOut.Len() != 0 {
		t.Errorf("Err = %q, want no warning", errOut.String())
	}
}

func TestAgentsMDWithoutGitChecksOnlyCwd(t *testing.T) {
	outer := t.TempDir()
	for dir := outer; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			t.Skipf("the temp dir is inside a repository (%s), so there's no .git-free tree to test", dir)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	cwd := filepath.Join(outer, "child")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	a := agentAt(t, cwd)
	base := firstSystem(t, a)
	writeTestFile(t, filepath.Join(outer, "AGENTS.md"), "parent rules")
	if got := firstSystem(t, a); got != base {
		t.Errorf("system = %q, want %q", got, base)
	}
}

// cachedModel reports cache reads and writes on every reply.
type cachedModel struct{ scripted }

func (c *cachedModel) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	msg, cost, err := c.scripted.Query(ctx, msgs, specs)
	cost.CacheReadTokens, cost.CacheWriteTokens = 70, 10
	return msg, cost, err
}

func TestRunTotalsCacheTokens(t *testing.T) {
	m := &cachedModel{scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "list_files", `{}`)}},
		{Role: "assistant", Content: "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}}
	tr := newAgent(t, m).Run(context.Background(), "x")
	if tr.CacheReadTokens != 140 || tr.CacheWriteTokens != 20 || tr.InputTokens != 200 {
		t.Errorf("totals = %d read / %d write / %d input, want 140 / 20 / 200", tr.CacheReadTokens, tr.CacheWriteTokens, tr.InputTokens)
	}
	b, _ := json.Marshal(tr)
	if !strings.Contains(string(b), `"cache_read_tokens":140`) || !strings.Contains(string(b), `"cache_write_tokens":20`) {
		t.Errorf("trajectory JSON lacks cache totals: %s", b)
	}

	plain := newAgent(t, &scripted{replies: []model.Message{{Role: "assistant", Content: "answer"}}}).Run(context.Background(), "x")
	if b, _ := json.Marshal(plain); strings.Contains(string(b), "cache_") {
		t.Errorf("trajectory JSON without caching has cache fields: %s", b)
	}
}

// TestAnthropicRunKeepsPrefixStable runs the real loop against a fake
// Anthropic server and checks the cacheable prefix never changes between steps.
func TestAnthropicRunKeepsPrefixStable(t *testing.T) {
	replies := []string{
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"list_files","input":{}}],"usage":{"input_tokens":5,"cache_creation_input_tokens":3000,"output_tokens":5}}`,
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"write_file","input":{"path":"a.txt","content":"hi"}}],"usage":{"input_tokens":5,"cache_read_input_tokens":3000,"cache_creation_input_tokens":50,"output_tokens":5}}`,
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"}],"usage":{"input_tokens":5,"cache_read_input_tokens":3050,"cache_creation_input_tokens":40,"output_tokens":5}}`,
	}
	var bodies []map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		io.WriteString(w, replies[len(bodies)-1])
	}))
	defer srv.Close()

	tr := newAgent(t, &model.Anthropic{BaseURL: srv.URL, Model: "claude-sonnet-4-5"}).Run(context.Background(), "x")
	if tr.ExitStatus != StatusSubmitted || len(bodies) != 3 {
		t.Fatalf("status %s after %d requests: %s", tr.ExitStatus, len(bodies), tr.Submission)
	}
	for i := 1; i < len(bodies); i++ {
		for _, k := range []string{"tools", "system", "cache_control"} {
			if len(bodies[0][k]) == 0 || string(bodies[i][k]) != string(bodies[0][k]) {
				t.Errorf("step %d %s changed or missing", i+1, k)
			}
		}
	}
	if tr.CacheReadTokens != 6050 || tr.CacheWriteTokens != 3090 || tr.InputTokens != 15+6050+3090 {
		t.Errorf("totals = %d read / %d write / %d input", tr.CacheReadTokens, tr.CacheWriteTokens, tr.InputTokens)
	}
}

// cancelling cancels ctx when queried, simulating Ctrl-C mid-run.
type cancelling struct {
	scripted
	cancel context.CancelFunc
	after  int
}

func (c *cancelling) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	if len(c.seen) >= c.after {
		c.cancel()
		return model.Message{}, model.Cost{}, ctx.Err()
	}
	return c.scripted.Query(ctx, msgs, specs)
}

func TestRunKillsBackgroundProcesses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{{"step limit", StatusStepLimit}, {"interrupt", StatusInterrupted}} {
		t.Run(tc.name, func(t *testing.T) {
			start := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "start_process", `{"command":"echo $$ > bg.pid; echo serving; sleep 300"}`)}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var m model.Model
			if tc.status == StatusInterrupted {
				m = &cancelling{scripted: scripted{replies: []model.Message{start}}, cancel: cancel, after: 1}
			} else {
				m = &scripted{replies: []model.Message{start}}
			}
			a := newAgent(t, m)
			if tc.status == StatusStepLimit {
				a.Config.StepLimit = 1
			}
			tr := a.Run(ctx, "start a server")
			if tr.ExitStatus != tc.status {
				t.Fatalf("status = %s", tr.ExitStatus)
			}
			if !strings.Contains(tr.BackgroundProcesses, "killed at run end") || !strings.Contains(tr.BackgroundProcesses, "serving") {
				t.Fatalf("BackgroundProcesses = %q", tr.BackgroundProcesses)
			}
			if s := a.Tools.Close(); s != "" {
				t.Fatalf("processes left after run: %q", s)
			}
		})
	}
}
