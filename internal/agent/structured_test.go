package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// specModel is scripted but also records the tool specs of each request.
type specModel struct {
	scripted
	specs [][]model.ToolSpec
}

func (s *specModel) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	s.specs = append(s.specs, specs)
	return s.scripted.Query(ctx, msgs, specs)
}

const testSchema = `{"$schema":"x","type":"object","properties":{"verdict":{"type":"string","enum":["pass","fail"]}},"required":["verdict"],"additionalProperties":false}`

func schemaAgent(t *testing.T, replies ...model.Message) (*Agent, *specModel) {
	t.Helper()
	s, err := outschema.Parse([]byte(testSchema))
	if err != nil {
		t.Fatal(err)
	}
	m := &specModel{scripted: scripted{replies: replies}}
	a := newAgent(t, m)
	a.SkipAgentsMD = true
	a.OutputSchema = s
	return a, m
}

func submitCall(id, args string) model.Message {
	return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call(id, SubmitToolName, args)}}
}

func TestDefaultRunHasNoSubmitTool(t *testing.T) {
	m := &specModel{scripted: scripted{replies: []model.Message{{Role: "assistant", Content: "done"}}}}
	a := newAgent(t, m)
	tr := a.Run(context.Background(), "x")
	if !reflect.DeepEqual(m.specs[0], a.Tools.Catalog()) || strings.Contains(tr.SystemPrompt, structuredOutputNotice) {
		t.Errorf("default run changed: specs %v", toolNames(m.specs[0]))
	}
	if tr.StructuredOutput != nil || tr.OutputSchemaDigest != "" || tr.OutputSchema != nil {
		t.Errorf("trajectory has structured fields: %+v", tr)
	}
}

func TestSubmitToolAdvertised(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		a, m := schemaAgent(t, submitCall("1", `{"verdict":"pass"}`))
		a.Tools.ReadOnly = readOnly
		tr := a.Run(context.Background(), "x")
		specs := m.specs[0]
		if len(specs) != len(a.Tools.Catalog())+1 {
			t.Fatalf("read-only %v: specs = %v", readOnly, toolNames(specs))
		}
		last := specs[len(specs)-1].Function
		want := map[string]any{}
		json.Unmarshal([]byte(testSchema), &want)
		delete(want, "$schema")
		if last.Name != SubmitToolName || !reflect.DeepEqual(last.Parameters, want) {
			t.Errorf("read-only %v: submit spec = %+v", readOnly, last)
		}
		if !strings.HasSuffix(tr.SystemPrompt, structuredOutputNotice) {
			t.Errorf("system prompt lacks notice: %q", tr.SystemPrompt)
		}
		if tr.ExitStatus != StatusSubmitted {
			t.Errorf("read-only %v: status = %s (%s)", readOnly, tr.ExitStatus, tr.Submission)
		}
	}
}

func TestValidSubmission(t *testing.T) {
	a, _ := schemaAgent(t, submitCall("1", `{ "verdict": "pass" }`))
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusSubmitted || string(tr.StructuredOutput) != `{"verdict":"pass"}` || tr.Submission != `{"verdict":"pass"}` {
		t.Errorf("trajectory = %+v", tr)
	}
	if tr.OutputSchemaDigest == "" || tr.OutputSchema == nil {
		t.Errorf("schema not recorded")
	}
	if last := tr.Messages[len(tr.Messages)-1]; last.Role != "tool" || last.ToolCallID != "1" {
		t.Errorf("last message = %+v", last)
	}
}

func TestInvalidSubmissionIsCorrected(t *testing.T) {
	a, m := schemaAgent(t, submitCall("1", `{}`), submitCall("2", `{"verdict":"fail"}`))
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusSubmitted || string(tr.StructuredOutput) != `{"verdict":"fail"}` || tr.Steps != 2 {
		t.Fatalf("trajectory = %+v", tr)
	}
	first := m.seen[1][len(m.seen[1])-1]
	if first.Role != "tool" || !strings.Contains(first.Content, `missing required property "verdict"`) {
		t.Errorf("first result = %+v", first)
	}
}

func TestRepeatedInvalidEndsInvalidOutput(t *testing.T) {
	cases := map[string]model.Message{
		"invalid args": submitCall("1", `{"verdict":"maybe"}`),
		"marker":       {Role: "assistant", Content: "{\"verdict\":\"pass\"}\n" + SubmitMarker},
		"no tool call": {Role: "assistant", Content: "done"},
	}
	for name, reply := range cases {
		replies := make([]model.Message, MaxInvalidOutputs+1)
		for i := range replies {
			replies[i] = reply
		}
		a, m := schemaAgent(t, replies...)
		out := filepath.Join(t.TempDir(), "t.json")
		a.OnStep = func(tr *trajectory.Trajectory) error { return tr.Save(out) }
		tr := a.Run(context.Background(), "x")
		if tr.ExitStatus != StatusInvalidOutput || tr.StructuredOutput != nil || tr.Steps != MaxInvalidOutputs {
			t.Errorf("%s: trajectory status %s steps %d output %s", name, tr.ExitStatus, tr.Steps, tr.StructuredOutput)
		}
		b, _ := os.ReadFile(out)
		if strings.Contains(string(b), "structured_output") {
			t.Errorf("%s: saved trajectory has structured_output", name)
		}
		if name != "invalid args" {
			if last := m.seen[1][len(m.seen[1])-1]; last.Role != "user" || last.Content != submitReminder {
				t.Errorf("%s: reminder = %+v", name, last)
			}
		}
	}
}

func TestOtherToolStepResetsInvalidCount(t *testing.T) {
	bad := submitCall("b", `{}`)
	other := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("l", "list_files", `{}`)}}
	a, _ := schemaAgent(t, bad, bad, other, bad, bad, submitCall("ok", `{"verdict":"pass"}`))
	if tr := a.Run(context.Background(), "x"); tr.ExitStatus != StatusSubmitted {
		t.Errorf("status = %s (%s)", tr.ExitStatus, tr.Submission)
	}
}

func TestMixedCallsDoNotEndRun(t *testing.T) {
	mixed := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{
		call("r", "read_file", `{"path":"f.txt"}`), call("s", SubmitToolName, `{"verdict":"pass"}`),
	}}
	a, m := schemaAgent(t, mixed, submitCall("s2", `{"verdict":"pass"}`))
	os.WriteFile(filepath.Join(a.Tools.Root, "f.txt"), []byte("hello"), 0o644)
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusSubmitted || tr.Steps != 2 {
		t.Fatalf("trajectory = %+v", tr)
	}
	sent := m.seen[1]
	r, s := sent[len(sent)-2], sent[len(sent)-1]
	if r.ToolCallID != "r" || !strings.Contains(r.Content, "hello") || s.ToolCallID != "s" || !strings.Contains(s.Content, "must be the only tool call") {
		t.Errorf("results = %+v / %+v", r, s)
	}
}

func TestStepLimitBeatsInvalidSubmission(t *testing.T) {
	a, _ := schemaAgent(t, submitCall("1", `{}`), submitCall("2", `{"verdict":"pass"}`))
	a.Config.StepLimit = 1
	if tr := a.Run(context.Background(), "x"); tr.ExitStatus != StatusStepLimit || tr.StructuredOutput != nil {
		t.Errorf("trajectory = %+v", tr)
	}
}

func TestHooksSkipSubmitTool(t *testing.T) {
	a, _ := schemaAgent(t,
		model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("l", "list_files", `{}`)}},
		submitCall("s", `{"verdict":"pass"}`))
	log := filepath.Join(t.TempDir(), "hooks.log")
	withHooks(t, a, config.Hooks{
		PreTool:  []config.Hook{hookstest.Hook(t, "", "log", log, "pre")},
		PostTool: []config.Hook{hookstest.Hook(t, "", "log", log, "post")},
	})
	var hookTools []string
	a.OnEvent = func(kind string, f map[string]any) {
		if kind == "hook" {
			hookTools = append(hookTools, f["tool_name"].(string))
		}
	}
	if tr := a.Run(context.Background(), "x"); tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	b, _ := os.ReadFile(log)
	if string(b) != "pre\npost\n" || strings.Join(hookTools, ",") != "list_files,list_files" {
		t.Errorf("hook log %q, hook events %v", b, hookTools)
	}
}

func TestValidationErrorsHaveNoEnvironment(t *testing.T) {
	t.Setenv("TINYAGENT_SECRET_TEST", "s3cr3t-value")
	a, _ := schemaAgent(t, submitCall("1", `{"verdict":"nope","x":1}`), submitCall("2", `{"verdict":"pass"}`))
	var events strings.Builder
	a.OnEvent = func(kind string, f map[string]any) {
		b, _ := json.Marshal(f)
		events.Write(b)
	}
	tr := a.Run(context.Background(), "x")
	all, _ := json.Marshal(tr)
	if strings.Contains(events.String(), "s3cr3t") || strings.Contains(string(all), "s3cr3t") {
		t.Error("environment value leaked")
	}
}

func TestResumeRefusedForSchemaRun(t *testing.T) {
	a, _ := schemaAgent(t, submitCall("1", `{}`))
	a.Config.StepLimit = 1
	tr := a.Run(context.Background(), "x")
	err := ValidateResume(tr, a.Config, a.Tools.Root)
	if err == nil || !strings.Contains(err.Error(), "--output-schema") {
		t.Errorf("err = %v", err)
	}
}
