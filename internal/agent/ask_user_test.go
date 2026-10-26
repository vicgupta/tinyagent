package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

const askArgs = `{"question":"Per key or per IP?","options":["per key","per IP"]}`

// askAgent returns an agent with ask_user on and a spec-recording model.
func askAgent(t *testing.T, replies ...model.Message) (*Agent, *specModel) {
	t.Helper()
	m := &specModel{scripted: scripted{replies: replies}}
	a := newAgent(t, m)
	a.SkipAgentsMD = true
	a.Tools.AskUser = true
	return a, m
}

// askedRun runs until a lone ask_user call ends the run NeedsInput.
func askedRun(t *testing.T) (*Agent, *trajectory.Trajectory) {
	t.Helper()
	a, _ := askAgent(t,
		assistantCalls(call("r", "read_file", `{"path":"missing"}`)),
		assistantCalls(call("q", "ask_user", askArgs)),
	)
	tr := a.Run(context.Background(), "add rate limiting")
	if tr.ExitStatus != StatusNeedsInput {
		t.Fatalf("setup: %s %q", tr.ExitStatus, tr.Submission)
	}
	return a, tr
}

func TestAskUserOffByDefault(t *testing.T) { // AC1
	m := &specModel{scripted: scripted{replies: []model.Message{{Role: "assistant", Content: "done"}}}}
	a := newAgent(t, m)
	tr := a.Run(context.Background(), "x")
	if slices.Contains(toolNames(m.specs[0]), tools.AskUserToolName) {
		t.Errorf("ask_user advertised by default: %v", toolNames(m.specs[0]))
	}
	if strings.Contains(tr.SystemPrompt, askUserNotice) {
		t.Error("system prompt has the ask_user notice by default")
	}
	b, _ := json.Marshal(tr)
	if strings.Contains(string(b), "ask_user") || strings.Contains(string(b), "question") {
		t.Errorf("trajectory JSON mentions ask_user: %s", b)
	}
	if tr.ToolSignature != ToolSignature() {
		t.Errorf("signature changed: %s", tr.ToolSignature)
	}
}

func TestAskUserEndsRunNeedsInput(t *testing.T) { // AC2
	a, m := askAgent(t, assistantCalls(call("q", "ask_user", askArgs)))
	var result map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		if kind == "tool.result" {
			result = f
		}
	}
	tr := a.Run(context.Background(), "add rate limiting")
	if !slices.Contains(toolNames(m.specs[0]), tools.AskUserToolName) || !strings.Contains(tr.SystemPrompt, askUserNotice) {
		t.Fatalf("ask_user not offered: %v", toolNames(m.specs[0]))
	}
	if tr.ExitStatus != StatusNeedsInput || tr.Submission != "Per key or per IP?" || tr.Steps != 1 {
		t.Fatalf("result: %s %q steps %d", tr.ExitStatus, tr.Submission, tr.Steps)
	}
	want := &trajectory.Question{ToolCallID: "q", Question: "Per key or per IP?", Options: []string{"per key", "per IP"}}
	if !reflect.DeepEqual(tr.PendingQuestion, want) || tr.QuestionsAsked != 1 {
		t.Fatalf("question: %+v asked %d", tr.PendingQuestion, tr.QuestionsAsked)
	}
	if len(tr.PendingCalls) != 1 || tr.PendingCalls[0] != (trajectory.PendingCall{ID: "q", Name: "ask_user"}) {
		t.Fatalf("pending: %+v", tr.PendingCalls)
	}
	if last := tr.Messages[len(tr.Messages)-1]; last.Role != "assistant" || result != nil {
		t.Fatalf("ask_user was answered: %+v %v", last, result)
	}
	if err := roundTrip(t, tr).Validate(); err != nil {
		t.Fatalf("NeedsInput checkpoint invalid: %v", err)
	}
}

func TestAskUserAnswerIsToolResult(t *testing.T) { // AC4
	a, tr := askedRun(t)
	tr = roundTrip(t, tr)
	before := append([]model.Message(nil), tr.Messages...)
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: "done\n" + SubmitMarker}}}
	a.Model, a.Resume, a.FollowUp = m, tr, "per key"
	got := a.Run(context.Background(), "")
	if len(m.seen) != 1 {
		t.Fatalf("queries: %d (%s %s)", len(m.seen), got.ExitStatus, got.Submission)
	}
	sent := m.seen[0]
	if len(sent) != len(before)+1 || !reflect.DeepEqual(sent[:len(before)], before) {
		t.Fatal("history not sent intact")
	}
	last := sent[len(sent)-1]
	if last.Role != "tool" || last.ToolCallID != "q" || !strings.HasPrefix(last.Content, trajectory.AnswerNotice) || !strings.HasSuffix(last.Content, "per key") {
		t.Fatalf("answer message: %+v", last)
	}
	r := got.Resume
	if r == nil || !r.Answer || r.FollowUp || r.ExitStatus != StatusNeedsInput || r.Message != len(before) || r.Steps != 2 {
		t.Fatalf("provenance: %+v", r)
	}
	if got.ExitStatus != StatusSubmitted || got.PendingQuestion != nil || got.PendingCalls != nil || got.QuestionsAsked != 1 || got.Steps != 3 {
		t.Fatalf("result: %+v", got)
	}
	if err := roundTrip(t, got).Validate(); err != nil {
		t.Fatalf("answered checkpoint invalid: %v", err)
	}
}

func TestAskUserAnswerRequired(t *testing.T) { // AC5
	a, tr := askedRun(t)
	m := &scripted{}
	a.Model, a.Resume = m, roundTrip(t, tr)
	if got := a.Run(context.Background(), ""); got.ExitStatus != StatusError || !strings.Contains(got.Submission, "answer") || len(m.seen) != 0 {
		t.Fatalf("no answer: %s %q", got.ExitStatus, got.Submission)
	}
}

func TestAskUserNotAlone(t *testing.T) { // AC6
	a, m := askAgent(t,
		assistantCalls(call("q", "ask_user", askArgs), call("w", "write_file", `{"path":"f","content":"x"}`)),
		model.Message{Role: "assistant", Content: "ok"},
	)
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusCompleted || len(m.seen) != 2 {
		t.Fatalf("result: %s, %d queries", tr.ExitStatus, len(m.seen))
	}
	res := toolResults(tr.Messages)
	if len(res) != 2 || res[0] != askUserAloneResult || res[1] != askUserAloneResult {
		t.Fatalf("results = %q", res)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "f")); err == nil {
		t.Fatal("write_file ran")
	}
	if tr.QuestionsAsked != 0 || tr.FormatErrors != 0 {
		t.Fatalf("counters: %+v", tr)
	}
}

func TestAskUserValidation(t *testing.T) { // AC7
	for _, args := range []string{
		`{}`,
		`{"question":"  "}`,
		`{"question":"q?","options":["one"]}`,
		`{"question":"q?","options":["1","2","3","4","5","6","7"]}`,
		`{"question":"q?","options":["a","a"]}`,
		`{"question":"q?","options":"a"}`,
	} {
		a, _ := askAgent(t, assistantCalls(call("q", "ask_user", args)), model.Message{Role: "assistant", Content: "ok"})
		a.Config.MaxFormatErrors = 1
		tr := a.Run(context.Background(), "x")
		res := toolResults(tr.Messages)
		if tr.ExitStatus != StatusFormatError || tr.FormatErrors != 1 || len(res) != 1 || !strings.HasPrefix(res[0], "format error:") {
			t.Errorf("%s: %s, format errors %d, results %q", args, tr.ExitStatus, tr.FormatErrors, res)
		}
	}
}

func TestAskUserBudget(t *testing.T) { // AC8
	a, tr := askedRun(t)
	a.Config.AskUser.MaxQuestions = 1
	m := &scripted{replies: []model.Message{
		assistantCalls(call("q2", "ask_user", `{"question":"And the limit?"}`)),
		{Role: "assistant", Content: "assumed 100/min"},
	}}
	a.Model, a.Resume, a.FollowUp = m, roundTrip(t, tr), "per key"
	got := a.Run(context.Background(), "")
	if got.ExitStatus != StatusCompleted || got.QuestionsAsked != 1 || got.PendingQuestion != nil {
		t.Fatalf("result: %s asked %d", got.ExitStatus, got.QuestionsAsked)
	}
	if res := toolResults(got.Messages); res[len(res)-1] != askUserBudgetResult {
		t.Fatalf("results = %q", res)
	}
	if err := roundTrip(t, got).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAskUserSkipsHooks(t *testing.T) { // AC10
	a, _ := askAgent(t, assistantCalls(call("q", "ask_user", askArgs)))
	withHooks(t, a, config.Hooks{
		PreTool:  []config.Hook{hookstest.Hook(t, "", "exit2", "denied")},
		PostTool: []config.Hook{hookstest.Hook(t, "", "stdout", `{"feedback":"post"}`)},
		Stop:     []config.Hook{hookstest.Hook(t, "", "exit2", "keep going")},
	})
	var hooksRan int
	a.OnEvent = func(kind string, _ map[string]any) {
		if kind == "hook" {
			hooksRan++
		}
	}
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusNeedsInput || hooksRan != 0 || tr.StopContinuations != 0 {
		t.Fatalf("%s, %d hooks ran", tr.ExitStatus, hooksRan)
	}
}

func TestAskUserNotOfferedToSubagents(t *testing.T) { // AC10
	a, _, cm := subagentAgent(t, []model.Message{
		assistantCalls(delegateCall("d", "look around")),
		{Role: "assistant", Content: "done"},
	}, []model.Message{{Role: "assistant", Content: "report" + submitMarker}})
	a.Tools.AskUser = true
	a.Run(context.Background(), "x")
	if len(cm.specs) == 0 || slices.Contains(toolNames(cm.specs[0]), tools.AskUserToolName) {
		t.Fatalf("child tools: %v", cm.specs)
	}
}

func TestAskUserReadOnly(t *testing.T) { // AC11
	a, m := askAgent(t, assistantCalls(call("q", "ask_user", askArgs)))
	a.Tools.ReadOnly = true
	tr := a.Run(context.Background(), "x")
	if !slices.Contains(toolNames(m.specs[0]), tools.AskUserToolName) || tr.ExitStatus != StatusNeedsInput {
		t.Fatalf("read-only: %s %v", tr.ExitStatus, toolNames(m.specs[0]))
	}
	if err := roundTrip(t, tr).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAskUserResumeSettingMustMatch(t *testing.T) { // AC12
	a, tr := askedRun(t)
	m := &scripted{}
	a.Model, a.Resume, a.FollowUp = m, roundTrip(t, tr), "per key"
	a.Tools.AskUser = false
	got := a.Run(context.Background(), "")
	if got.ExitStatus != StatusError || len(m.seen) != 0 {
		t.Fatalf("mismatch: %s %q", got.ExitStatus, got.Submission)
	}
}

func TestAskUserCheckpointValidation(t *testing.T) {
	_, tr := askedRun(t)
	for name, mutate := range map[string]func(*trajectory.Trajectory){
		"no question":      func(c *trajectory.Trajectory) { c.PendingQuestion = nil },
		"wrong id":         func(c *trajectory.Trajectory) { c.PendingQuestion.ToolCallID = "r" },
		"setting off":      func(c *trajectory.Trajectory) { c.AskUserEnabled = false },
		"not NeedsInput":   func(c *trajectory.Trajectory) { c.ExitStatus = StatusInterrupted },
		"no pending calls": func(c *trajectory.Trajectory) { c.PendingCalls = nil },
	} {
		c := roundTrip(t, tr)
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: checkpoint accepted", name)
		}
	}
}
