package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// reply is one canned response from fakeModel.
type reply struct {
	msg   model.Message
	input int // reported input tokens; 0 means 100
	err   error
}

// request is one call fakeModel received.
type request struct {
	msgs  []model.Message
	tools []model.ToolSpec
}

// fakeModel replays replies for agent steps and answers summarization
// requests (sent with no tools) with a fixed summary.
type fakeModel struct {
	replies []reply
	reqs    []request
	summary string
}

func (f *fakeModel) Query(_ context.Context, msgs []model.Message, tools []model.ToolSpec) (model.Message, model.Cost, error) {
	f.reqs = append(f.reqs, request{append([]model.Message(nil), msgs...), tools})
	if tools == nil {
		return model.Message{Role: "assistant", Content: f.summary}, model.Cost{USD: 0.01, InputTokens: 500, OutputTokens: 50}, nil
	}
	if len(f.replies) == 0 {
		return model.Message{}, model.Cost{}, errors.New("out of replies")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	if r.err != nil {
		return model.Message{}, model.Cost{}, r.err
	}
	in := r.input
	if in == 0 {
		in = 100
	}
	return r.msg, model.Cost{USD: 0.01, InputTokens: in, OutputTokens: 20}, nil
}

// agentRequests returns the requests sent with tools, i.e. agent steps.
func (f *fakeModel) agentRequests() []request {
	var out []request
	for _, r := range f.reqs {
		if r.tools != nil {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeModel) summaryRequests() int { return len(f.reqs) - len(f.agentRequests()) }

// reads returns an assistant turn calling read_file on big.txt n times.
func reads(step, n int) model.Message {
	m := model.Message{Role: "assistant", Content: fmt.Sprintf("step %d", step)}
	for i := range n {
		m.ToolCalls = append(m.ToolCalls, call(fmt.Sprintf("c%d-%d", step, i), "read_file", `{"path":"big.txt"}`))
	}
	return m
}

var submit = model.Message{Role: "assistant", Content: "done\n" + SubmitMarker}

// bigContent is ~2,000 tokens at 4 chars per token.
var bigContent = strings.Repeat("x", 7999) + "\n"

func compactAgent(t *testing.T, f *fakeModel, window int) (*Agent, *strings.Builder) {
	a := newAgent(t, f)
	a.ModelName = "fake"
	a.Config.ContextWindow = window
	if err := os.WriteFile(filepath.Join(a.Tools.Root, "big.txt"), []byte(bigContent), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	a.Err = &stderr
	return a, &stderr
}

// checkPairing fails if any tool call lacks its result or any result lacks
// its call, in any request sent.
func checkPairing(t *testing.T, reqs []request) {
	t.Helper()
	for n, r := range reqs {
		open := map[string]bool{}
		for i, m := range r.msgs {
			for _, c := range m.ToolCalls {
				open[c.ID] = true
			}
			if m.Role == "tool" {
				if !open[m.ToolCallID] {
					t.Errorf("request %d message %d: tool result %s without its call", n, i, m.ToolCallID)
				}
				delete(open, m.ToolCallID)
			}
			if m.Role == "assistant" && i+1 < len(r.msgs) && len(open) > 0 && len(m.ToolCalls) == 0 {
				t.Errorf("request %d: calls %v without results", n, open)
			}
		}
		if len(open) > 0 {
			t.Errorf("request %d: calls %v without results", n, open)
		}
	}
}

func TestCompactElidesOldToolOutputs(t *testing.T) {
	f := &fakeModel{replies: []reply{
		{msg: reads(1, 1)}, {msg: reads(2, 1)}, {msg: reads(3, 1)},
		{msg: reads(4, 1)}, {msg: reads(5, 1)}, {msg: reads(6, 1), input: 15000},
		{msg: submit},
	}}
	a, stderr := compactAgent(t, f, 20000)
	a.ModelName = "openai/gpt-4o" // 128K in the table; the config value wins
	tr := a.Run(context.Background(), "the task")

	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status = %s: %s", tr.ExitStatus, tr.Submission)
	}
	if f.summaryRequests() != 0 {
		t.Errorf("summary requests = %d, want 0", f.summaryRequests())
	}
	reqs := f.agentRequests()
	last := reqs[len(reqs)-1].msgs
	if !reflect.DeepEqual(last[:2], tr.Messages[:2]) || last[1].Content != "the task" {
		t.Errorf("system prompt or task changed: %+v", last[:2])
	}
	// Messages: 0 system, 1 task, then (assistant, tool) per step.
	for step := 1; step <= 6; step++ {
		got := last[2*step+1].Content
		elided := strings.HasPrefix(got, elidedPrefix)
		if want := step <= 4; elided != want {
			t.Errorf("step %d output elided = %v, want %v (%.40q)", step, elided, want, got)
		}
		if last[2*step].Content != fmt.Sprintf("step %d", step) || len(last[2*step].ToolCalls) != 1 {
			t.Errorf("step %d assistant message changed: %+v", step, last[2*step])
		}
	}
	if !strings.Contains(last[3].Content, "8,000 chars") {
		t.Errorf("placeholder = %q", last[3].Content)
	}
	checkPairing(t, f.reqs)

	// The trajectory keeps every original output and records the compaction.
	for i, m := range tr.Messages {
		if m.Role == "tool" && m.Content != bigContent {
			t.Errorf("trajectory message %d was modified: %.40q", i, m.Content)
		}
	}
	if len(tr.Compactions) != 1 {
		t.Fatalf("compactions = %+v", tr.Compactions)
	}
	c := tr.Compactions[0]
	if c.Stage != "elide" || c.Reason != "proactive" || c.Step != 6 || c.TokensAfter >= c.TokensBefore ||
		!reflect.DeepEqual(c.Elided, []int{3, 5, 7, 9}) || len(c.View) != 14 {
		t.Errorf("compaction = %+v", c)
	}
	if !strings.Contains(stderr.String(), "compacted context: ~17,020 → ~") || !strings.Contains(stderr.String(), "elided 4 tool outputs") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestCompactSummarizes(t *testing.T) {
	f := &fakeModel{summary: "## Task\nread big.txt", replies: []reply{
		{msg: reads(1, 1)}, {msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000},
		{msg: submit},
	}}
	a, stderr := compactAgent(t, f, 30000)
	out := filepath.Join(t.TempDir(), "traj.json")
	a.OnStep = func(tr *trajectory.Trajectory) {
		if err := tr.Save(out); err != nil {
			t.Fatal(err)
		}
	}
	tr := a.Run(context.Background(), "the task")

	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status = %s: %s", tr.ExitStatus, tr.Submission)
	}
	if n := f.summaryRequests(); n != 1 {
		t.Fatalf("summary requests = %d, want 1", n)
	}
	var sum request
	for _, r := range f.reqs {
		if r.tools == nil {
			sum = r
		}
	}
	if len(sum.msgs) != 2 || !strings.Contains(sum.msgs[1].Content, "step 1") || strings.Contains(sum.msgs[1].Content, "step 3") ||
		!strings.Contains(sum.msgs[1].Content, "## Next steps") {
		t.Errorf("summary request = %+v", sum.msgs)
	}

	reqs := f.agentRequests()
	last := reqs[len(reqs)-1].msgs
	// system, task, summary, step 2 (3 calls + 3 results), step 3 (1 + 1).
	if len(last) != 9 || !reflect.DeepEqual(last[:2], tr.Messages[:2]) {
		t.Fatalf("next request = %d messages: %+v", len(last), last)
	}
	if last[2].Role != "user" || !strings.HasPrefix(last[2].Content, summaryHeader) || !strings.HasSuffix(last[2].Content, f.summary) {
		t.Errorf("summary message = %+v", last[2])
	}
	if last[3].Content != "step 2" || len(last[3].ToolCalls) != 3 || last[7].Content != "step 3" || last[8].Content != bigContent {
		t.Errorf("recent turns not verbatim: %+v", last[3:])
	}
	checkPairing(t, f.reqs)

	// Totals include the summary call (USD 0.01, 500 in / 50 out) but not as a step.
	if tr.Steps != 4 || tr.InputTokens != 100+100+23000+100+500 || tr.OutputTokens != 4*20+50 {
		t.Errorf("steps %d, tokens %d / %d", tr.Steps, tr.InputTokens, tr.OutputTokens)
	}
	if fmt.Sprintf("%.2f", tr.Cost) != "0.05" {
		t.Errorf("cost = %v, want 0.05", tr.Cost)
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var saved trajectory.Trajectory
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Messages) != 2+2+4+2+1 || len(saved.Compactions) != 1 {
		t.Fatalf("saved %d messages, %d compactions", len(saved.Messages), len(saved.Compactions))
	}
	c := saved.Compactions[0]
	if c.Stage != "summarize" || c.Step != 3 || c.Summary != f.summary || !reflect.DeepEqual(c.Summarized, []int{2, 3}) ||
		!reflect.DeepEqual(c.View, []int{0, 1, -1, 4, 5, 6, 7, 8, 9}) || c.TokensAfter >= c.TokensBefore || c.USD <= 0 {
		t.Errorf("compaction = %+v", c)
	}
	if !strings.Contains(stderr.String(), "summarized 2 messages") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestCompactSummaryCountsTowardCostLimit(t *testing.T) {
	f := &fakeModel{summary: "s", replies: []reply{
		{msg: reads(1, 1)}, {msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000}, {msg: submit},
	}}
	a, _ := compactAgent(t, f, 30000)
	a.Config.CostLimit = 0.035
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusCostLimit || tr.Steps != 3 || f.summaryRequests() != 1 {
		t.Errorf("status %s after %d steps, %d summaries", tr.ExitStatus, tr.Steps, f.summaryRequests())
	}
}

func TestCompactOnOverflowError(t *testing.T) {
	for _, msg := range []string{
		`HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 201000 tokens > 200000 maximum"}}`,
		`HTTP 400: {"error":{"message":"too long","code":"context_length_exceeded"}}`,
	} {
		f := &fakeModel{replies: []reply{
			{msg: reads(1, 1)}, {msg: reads(2, 1)}, {msg: reads(3, 1)},
			{err: errors.New(msg)}, {msg: submit},
		}}
		a, _ := compactAgent(t, f, 0) // window unknown: reactive only
		tr := a.Run(context.Background(), "x")
		if tr.ExitStatus != StatusSubmitted {
			t.Fatalf("%s: status = %s: %s", msg, tr.ExitStatus, tr.Submission)
		}
		reqs := f.agentRequests()
		if len(reqs) != 5 {
			t.Fatalf("requests = %d, want 5", len(reqs))
		}
		if !strings.HasPrefix(reqs[4].msgs[3].Content, elidedPrefix) || strings.HasPrefix(reqs[3].msgs[3].Content, elidedPrefix) {
			t.Errorf("retry not compacted")
		}
		if len(tr.Compactions) != 1 || tr.Compactions[0].Reason != "overflow" {
			t.Errorf("compactions = %+v", tr.Compactions)
		}
		checkPairing(t, f.reqs)
	}
}

func TestOverflowAfterCompactionEndsRun(t *testing.T) {
	overflow := errors.New("HTTP 400: prompt is too long")
	f := &fakeModel{replies: []reply{
		{msg: reads(1, 1)}, {msg: reads(2, 1)}, {msg: reads(3, 1)},
		{err: overflow}, {err: overflow}, {msg: submit},
	}}
	a, _ := compactAgent(t, f, 0)
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusError || !strings.Contains(tr.Submission, "context still exceeds the window after compaction") {
		t.Errorf("status = %s: %s", tr.ExitStatus, tr.Submission)
	}
	if n := len(f.agentRequests()); n != 5 {
		t.Errorf("requests = %d, want 5 (one retry only)", n)
	}
}

func TestNoCompact(t *testing.T) {
	f := &fakeModel{replies: []reply{
		{msg: reads(1, 1)}, {msg: reads(2, 1)}, {msg: reads(3, 1), input: 25000},
		{err: errors.New("HTTP 400: prompt is too long")}, {msg: submit},
	}}
	a, stderr := compactAgent(t, f, 20000)
	a.NoCompact = true
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusError || !strings.Contains(tr.Submission, "prompt is too long") {
		t.Errorf("status = %s: %s", tr.ExitStatus, tr.Submission)
	}
	if len(f.reqs) != 4 || len(tr.Compactions) != 0 || stderr.Len() != 0 {
		t.Errorf("%d requests, %d compactions, stderr %q", len(f.reqs), len(tr.Compactions), stderr.String())
	}
	for i, r := range f.reqs {
		if want := tr.Messages[:len(r.msgs)]; !reflect.DeepEqual(r.msgs, want) || len(r.msgs) != 2+2*i {
			t.Errorf("request %d differs from the full history", i)
		}
	}
}

func TestNoProactiveCompactionWithoutWindow(t *testing.T) {
	f := &fakeModel{replies: []reply{
		{msg: reads(1, 1)}, {msg: reads(2, 1)}, {msg: reads(3, 1), input: 900000}, {msg: submit},
	}}
	a, _ := compactAgent(t, f, 0)
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusSubmitted || len(tr.Compactions) != 0 || f.summaryRequests() != 0 {
		t.Errorf("status %s, compactions %+v", tr.ExitStatus, tr.Compactions)
	}
}

func TestContextWindowConfigOverridesTable(t *testing.T) {
	a := &Agent{ModelName: "anthropic/claude-sonnet-4"}
	if w := a.contextWindow(); w != 200_000 {
		t.Errorf("table window = %d", w)
	}
	a.Config.ContextWindow = 50_000
	if w := a.contextWindow(); w != 50_000 {
		t.Errorf("config window = %d", w)
	}
}

func TestProtectedFromKeepsToolResultsWithCalls(t *testing.T) {
	tool := func(id string) model.Message { return model.Message{Role: "tool", ToolCallID: id, Content: bigContent} }
	v := newView([]model.Message{
		{Role: "system"}, {Role: "user"},
		reads(1, 1), tool("c1-0"),
		reads(2, 3), tool("c2-0"), tool("c2-1"), tool("c2-2"),
		reads(3, 1), tool("c3-0"),
		reads(4, 2), tool("c4-0"), tool("c4-1"),
	})
	for _, keep := range []int{0, 3000, 5000, 9000, 1 << 30} {
		b := v.protectedFrom(keep)
		if b < 2 || b > len(v.msgs)-5 || v.msgs[b].Role != "assistant" {
			t.Errorf("keep %d: boundary %d at %s", keep, b, v.msgs[b].Role)
		}
	}
	if b := v.protectedFrom(0); b != 8 {
		t.Errorf("minimum boundary = %d, want 8 (last 2 exchanges)", b)
	}
	if b := v.protectedFrom(1 << 30); b != 2 {
		t.Errorf("unbounded boundary = %d, want 2", b)
	}
}
