package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func call(id, name, args string) model.ToolCall {
	return model.ToolCall{ID: id, Type: "function", Function: model.FunctionCall{Name: name, Arguments: args}}
}

// finished is a two-step Submitted run: one bash call, then the submission.
func finished() *trajectory.Trajectory {
	return &trajectory.Trajectory{
		SchemaVersion: 2, Task: "fix the test", Model: "openai/gpt-4o", ExitStatus: "Submitted",
		Steps: 2, InputTokens: 48213, OutputTokens: 3104, CacheReadTokens: 1000, Cost: 0.23,
		Submission: "fixed it",
		Messages: []model.Message{
			{Role: "system", Content: "you are tinyagent"},
			{Role: "user", Content: "fix the test"},
			{Role: "assistant", Content: "Running the tests.", ToolCalls: []model.ToolCall{call("c1", "bash", `{"command":"go test ./..."}`)}},
			{Role: "tool", ToolCallID: "c1", Content: "ok"},
			{Role: "assistant", Content: "fixed it\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
		},
	}
}

func render(t *testing.T, tr *trajectory.Trajectory, opts Options) string {
	t.Helper()
	var b bytes.Buffer
	if err := Render(&b, tr, opts); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestFinishedRun(t *testing.T) { // AC1
	out := render(t, finished(), Options{})
	mustContain(t, out, "fix the test", "`openai/gpt-4o`", "`Submitted`", "**Steps:** 2",
		"48,213 input / 3,104 output", "1,000 cache read", "$0.23",
		"### Step 1\n", "### Step 2\n", "**Tool call:** `bash`", "\"command\": \"go test ./...\"",
		"## Submission\n\n```text\nfixed it\n```")
	if strings.Count(out, "### Step ") != 2 {
		t.Errorf("want 2 step headings:\n%s", out)
	}
	if strings.Contains(out, "you are tinyagent") {
		t.Error("system prompt rendered")
	}
}

func TestToolCallsInOrder(t *testing.T) { // AC1
	tr := finished()
	tr.Messages[2].ToolCalls = []model.ToolCall{call("a", "read_file", `{}`), call("b", "grep", `{}`)}
	tr.Messages[3] = model.Message{Role: "tool", ToolCallID: "a", Content: "1"}
	tr.Messages = append(tr.Messages[:4], append([]model.Message{{Role: "tool", ToolCallID: "b", Content: "2"}}, tr.Messages[4:]...)...)
	out := render(t, tr, Options{})
	if i, j := strings.Index(out, "`read_file`"), strings.Index(out, "`grep`"); i < 0 || j < i {
		t.Errorf("tool calls out of order:\n%s", out)
	}
}

func TestTruncation(t *testing.T) { // AC2
	for _, n := range []int{5000, 500000} {
		tr := finished()
		tr.Messages[3].Content = strings.Repeat("a", n)
		tr.Messages[2].ToolCalls[0].Function.Arguments = `{"command":"` + strings.Repeat("b", n) + `"}`
		out := render(t, tr, Options{})
		mustContain(t, out, fmt.Sprintf("[... %s characters omitted ...]", model.FormatBytes(n-DefaultLimit)))
		if strings.Contains(out, strings.Repeat("a", DefaultLimit+1)) || strings.Contains(out, strings.Repeat("b", DefaultLimit)) {
			t.Errorf("%d: output kept more than the limit", n)
		}
		if len(out) > 3*DefaultLimit+2000 {
			t.Errorf("%d: output is %d bytes", n, len(out))
		}
	}
	if out := render(t, finished(), Options{Limit: 1}); !strings.Contains(out, "```text\no\n[... 1 characters omitted ...]") {
		t.Errorf("custom limit not applied:\n%s", out)
	}
}

// outsideCode returns the parts of md a CommonMark renderer would treat as
// Markdown: lines outside fenced code blocks, with inline code spans removed.
// It fails on an unclosed fence.
func outsideCode(t *testing.T, md string) string {
	t.Helper()
	var b strings.Builder
	fence := 0
	for _, line := range strings.Split(md, "\n") {
		run := len(line) - len(strings.TrimLeft(line, "`"))
		if fence > 0 {
			if run >= fence && strings.TrimSpace(line[run:]) == "" {
				fence = 0
			}
			continue
		}
		if run >= 3 && !strings.Contains(line[run:], "`") {
			fence = run
			continue
		}
		b.WriteString(stripSpans(line) + "\n")
	}
	if fence > 0 {
		t.Fatalf("unclosed fence in:\n%s", md)
	}
	return b.String()
}

func stripSpans(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		n := 0
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		open := strings.Repeat("`", n)
		end := -1
		for j := i + n; j < len(line); {
			k := strings.Index(line[j:], open)
			if k < 0 {
				break
			}
			k += j
			m := 0
			for k+m < len(line) && line[k+m] == '`' {
				m++
			}
			if m == n {
				end = k
				break
			}
			j = k + m
		}
		if end < 0 {
			b.WriteString(open)
			i += n
			continue
		}
		i = end + n
	}
	return b.String()
}

func TestHostileMarkdownIsInert(t *testing.T) { // AC3
	const hostile = "![x](https://example.invalid/?d=1) [link](https://example.invalid) <img src=x>\n```\n" +
		"# Fake heading\n````\n`````` trailing"
	tr := finished()
	tr.Task = hostile
	tr.Model = "evil`<img src=x>`model"
	tr.Messages[1].Content = hostile
	tr.Messages[2].Content = hostile
	tr.Messages[2].ToolCalls[0].Function.Name = "<img src=x>"
	tr.Messages[2].ToolCalls[0].Function.Arguments = hostile
	tr.Messages[3].Content = hostile
	tr.Submission = hostile
	tr.Plan = &trajectory.Plan{Version: 1, Items: []trajectory.PlanItem{{ID: "1", Text: hostile, Status: "done"}}, Note: hostile}
	tr.PlanHistory = []trajectory.PlanChange{{Step: 1, Version: 1, Diff: hostile}}
	tr.Compactions = []trajectory.Compaction{{Step: 1, Reason: hostile, Stage: "summarize", Summary: hostile}}
	tr.ModelSwitches = []trajectory.ModelSwitch{{Step: 2, From: hostile, To: hostile, Reason: hostile}}
	tr.Subagents = []trajectory.Subagent{{ID: "sa1", ToolCallID: "c1", Task: hostile, Label: hostile, Model: hostile, Status: "Submitted"}}
	out := render(t, tr, Options{})
	md := outsideCode(t, out)
	for _, bad := range []string{"](", "<img", "# Fake", "example.invalid"} {
		if strings.Contains(md, bad) {
			t.Errorf("%q is outside code:\n%s", bad, md)
		}
	}
	for _, h := range []string{"### Step 1", "### Step 2", "## Submission", "## Task"} {
		if !strings.Contains(md, h+"\n") {
			t.Errorf("structure lost: no %q outside code:\n%s", h, md)
		}
	}
	mustContain(t, out, "<summary>Result of &lt;img src=x&gt;</summary>")
}

func TestRedaction(t *testing.T) { // AC4
	const secret, gh = "supersecretvalue", "ghp_abcdefghijklmnopqrstuvwxyz0123"
	tr := finished()
	tr.Messages[3].Content = "token " + secret + " and " + gh
	tr.Messages[2].ToolCalls[0].Function.Arguments = `{"command":"echo ` + secret + `"}`
	tr.Submission = secret
	out := render(t, tr, Options{Redactor: eventlog.NewRedactor([]string{"FOO_TOKEN=" + secret})})
	if strings.Contains(out, secret) || strings.Contains(out, gh) {
		t.Errorf("secret leaked:\n%s", out)
	}
	mustContain(t, out, "token [REDACTED] and [REDACTED]", "echo [REDACTED]")
}

func TestImagesArePlaceholders(t *testing.T) { // AC5
	img := model.NewImage("image/png", "shot.png", bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 300))
	tr := finished()
	tr.Messages[1].Images = []model.Image{img}
	tr.Messages[1].Content += model.AttachmentLabels(tr.Messages[1].Images)
	tr.Messages[3].Images = []model.Image{img}
	out := render(t, tr, Options{})
	if strings.Contains(out, img.Data[:40]) {
		t.Errorf("image data rendered:\n%s", out)
	}
	if strings.Count(out, "[image: image/png, shot.png, 1,200 bytes]") != 2 {
		t.Errorf("want two placeholders:\n%s", out)
	}
}

func TestEventsAtTheirStep(t *testing.T) { // AC6
	tr := finished()
	// Step 1 ran, the run stopped, was resumed, then compacted, switched
	// model and updated the plan in step 2.
	tr.Messages = []model.Message{
		tr.Messages[0], tr.Messages[1], tr.Messages[2], tr.Messages[3],
		{Role: "user", Content: trajectory.ContinuationNotice},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("p", "update_plan", `{}`)}},
		{Role: "tool", ToolCallID: "p", Content: "plan updated"},
		{Role: "user", Content: trajectory.FollowUpNotice + "\n\nnow add docs"},
		{Role: "assistant", Content: "docs\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}
	tr.Steps = 3
	tr.Resume = &trajectory.ResumeInfo{Source: "b.json", ExitStatus: "Submitted", Steps: 2, Message: 7, FollowUp: true,
		Previous: &trajectory.ResumeInfo{Source: "a.json", ExitStatus: "StepLimitExceeded", Steps: 1, Message: 4}}
	tr.Compactions = []trajectory.Compaction{{Step: 1, Reason: "proactive", Stage: "elide", TokensBefore: 9000, TokensAfter: 3000, Elided: []int{3}}}
	tr.ModelSwitches = []trajectory.ModelSwitch{{Step: 2, From: "openai/gpt-4o", To: "openai/gpt-4o-mini", Reason: "429"}}
	tr.Plan = &trajectory.Plan{Version: 1, Items: []trajectory.PlanItem{{ID: "1", Text: "write code", Status: "done"}, {ID: "2", Text: "docs", Status: "pending"}}, UpdatedStep: 2}
	tr.PlanHistory = []trajectory.PlanChange{{Step: 2, Version: 1, Diff: "+ 1. write code"}}
	out := render(t, tr, Options{})
	order := []string{"### Step 1", "Running the tests.", "**Resumed** after `StepLimitExceeded` at step 1 (from `a.json`)",
		"**Compaction** after step 1", "**Model switch** before step 2", "### Step 2", "Result of update_plan",
		"**Follow-up** after `Submitted` at step 2 (from `b.json`)", "now add docs", "**Plan updated** to version 1 at step 2",
		"[x] 1. write code (done)\n[ ] 2. docs (pending)", "### Step 3"}
	at := 0
	for _, s := range order {
		i := strings.Index(out[at:], s)
		if i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", s, out)
		}
		at += i
	}
	// The elided message is still rendered in full.
	mustContain(t, out, "Result of bash</summary>\n\n```text\nok\n```")
}

func TestUnfinishedRun(t *testing.T) { // AC7
	tr := finished()
	tr.ExitStatus, tr.Submission = "Interrupted", ""
	tr.Messages = append(tr.Messages[:3:3], model.Message{Role: "assistant"})
	tr.Messages[2].ToolCalls = append(tr.Messages[2].ToolCalls, call("c2", "grep", `{}`))
	tr.Messages = tr.Messages[:3]
	tr.PendingCalls = []trajectory.PendingCall{{ID: "c1", Name: "bash"}, {ID: "c2", Name: "grep"}}
	out := render(t, tr, Options{})
	mustContain(t, out, "`Interrupted`", "_No result recorded for `bash` (call `c1`)._", "_No result recorded for `grep` (call `c2`)._")

	v1 := &trajectory.Trajectory{SchemaVersion: 1, Task: "x", Model: "m", ExitStatus: "StepLimitExceeded", Steps: 1,
		Messages: []model.Message{{Role: "system"}, {Role: "user", Content: "x"}, {Role: "assistant", Content: "hm"}}}
	mustContain(t, render(t, v1, Options{}), "`StepLimitExceeded`", "### Step 1")
	mustContain(t, render(t, &trajectory.Trajectory{}, Options{}), "`unfinished`", "## Task")
}

func TestDeterministic(t *testing.T) { // AC10
	tr := finished()
	tr.Snapshots = []trajectory.Snapshot{{ID: "s0", Kind: "baseline", Time: "2026-10-26T10:00:00Z", Files: 3}}
	a, b := render(t, tr, Options{}), render(t, tr, Options{})
	if a != b {
		t.Error("output differs between renders")
	}
	mustContain(t, a, "- `s0`: `baseline`, step 0, 3 files", "--rewind")
	if strings.Contains(a, "2026") {
		t.Error("snapshot time rendered")
	}
}

func TestStructuredOutput(t *testing.T) { // AC11
	tr := finished()
	tr.StructuredOutput = json.RawMessage(`{"ok":true}`)
	tr.Submission = `{"ok":true}`
	mustContain(t, render(t, tr, Options{}), "## Structured output\n\n```json\n{\n  \"ok\": true\n}\n```")
}

func TestReadIsLenient(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// No skip_agents_md, an unknown field, schema 0: resume refuses it.
	p := write("old.json", `{"task":"x","model":"m","messages":[],"some_future_field":1}`)
	if _, err := trajectory.Load(p); err == nil {
		t.Fatal("Load accepted the file")
	}
	if tr, err := trajectory.Read(p); err != nil || tr.Task != "x" {
		t.Fatalf("Read: %v", err)
	}
	for name, data := range map[string]string{"empty.json": " \n", "bad.json": `{"task": "sk-secret`, "v99.json": `{"schema_version":99}`, "null.json": "null"} {
		_, err := trajectory.Read(write(name, data))
		if err == nil || strings.Contains(err.Error(), "sk-secret") {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestPendingQuestion(t *testing.T) {
	tr := finished()
	tr.Messages[4] = model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("q", "ask_user", `{"question":"Per key or per IP?"}`)}}
	tr.ExitStatus, tr.Submission = "NeedsInput", "Per key or per IP?"
	tr.PendingQuestion = &trajectory.Question{ToolCallID: "q", Question: "Per key or per IP?", Options: []string{"per key", "per `IP`"}}
	out := render(t, tr, Options{})
	mustContain(t, out, "`NeedsInput`", "_No result recorded for `ask_user` (call `q`)._",
		"## Question for the user\n\n```text\nPer key or per IP?\n```", "- `per key`\n", "--resume <trajectory.json>")
	if strings.Contains(out, "## Final message") {
		t.Errorf("question rendered as a final message:\n%s", out)
	}
}
