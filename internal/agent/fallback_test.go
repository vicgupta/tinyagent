package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

// failing always fails with err.
type failing struct {
	err   error
	calls int
}

func (f *failing) Query(context.Context, []model.Message, []model.ToolSpec) (model.Message, model.Cost, error) {
	f.calls++
	return model.Message{}, model.Cost{}, f.err
}

func outage() error {
	return &model.RetryExhaustedError{Attempts: 6, Reason: "http_503", Err: errors.New("HTTP 503: overloaded")}
}

func TestRunFallbackAfterOutage(t *testing.T) {
	primary := &failing{err: outage()}
	fb := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "list_files", `{}`)}},
		{Role: "assistant", Content: "Done.\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}
	a := newAgent(t, model.NewFallback(model.FallbackEntry{Spec: "openai/a", Model: primary}, model.FallbackEntry{Spec: "anthropic/b", Model: fb}))
	a.ModelName, a.FallbackModels = "openai/a", []string{"anthropic/b"}
	var stderr strings.Builder
	a.Err = &stderr
	var events []map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		if kind == "model.fallback" {
			events = append(events, f)
		}
	}
	tr := a.Run(context.Background(), "go")
	if tr.ExitStatus != StatusSubmitted || primary.calls != 1 || len(fb.seen) != 2 {
		t.Fatalf("status %s (%s), primary calls %d, fallback calls %d", tr.ExitStatus, tr.Submission, primary.calls, len(fb.seen))
	}
	if tr.Model != "openai/a" || tr.ActiveModel != "anthropic/b" || len(tr.FallbackModels) != 1 {
		t.Errorf("model %q active %q fallbacks %v", tr.Model, tr.ActiveModel, tr.FallbackModels)
	}
	if len(tr.ModelSwitches) != 1 || tr.ModelSwitches[0].Step != 1 || tr.ModelSwitches[0].From != "openai/a" || tr.ModelSwitches[0].To != "anthropic/b" || tr.ModelSwitches[0].Reason != "http_503" {
		t.Errorf("switches = %+v", tr.ModelSwitches)
	}
	if !strings.Contains(stderr.String(), "model fallback: openai/a -> anthropic/b (http_503) at step 1\n") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if len(events) != 1 || events[0]["step"] != 1 || events[0]["to"] != "anthropic/b" || events[0]["attempts"] != 6 || events[0]["reason"] != "http_503" {
		t.Errorf("events = %+v", events)
	}
	for _, m := range tr.Messages {
		if m.Role == "assistant" && m.Extra[model.ExtraModel] != "anthropic/b" {
			t.Errorf("assistant message without serving model: %+v", m)
		}
	}
}

func TestRunFallbackExhaustedNamesModels(t *testing.T) {
	a := newAgent(t, model.NewFallback(model.FallbackEntry{Spec: "openai/a", Model: &failing{err: outage()}}, model.FallbackEntry{Spec: "anthropic/b", Model: &failing{err: outage()}}))
	a.ModelName, a.FallbackModels = "openai/a", []string{"anthropic/b"}
	tr := a.Run(context.Background(), "go")
	if tr.ExitStatus != StatusError || !strings.Contains(tr.Submission, "openai/a (http_503), anthropic/b (http_503)") || tr.Steps != 0 {
		t.Errorf("status %s: %s", tr.ExitStatus, tr.Submission)
	}
}

func TestRunWithoutFallbackUnchanged(t *testing.T) {
	a := newAgent(t, &scripted{replies: []model.Message{{Role: "assistant", Content: "answer"}}})
	tr := a.Run(context.Background(), "q")
	if tr.ActiveModel != "" || tr.FallbackModels != nil || tr.ModelSwitches != nil {
		t.Errorf("fallback fields set: %+v", tr)
	}
	if _, ok := tr.Messages[len(tr.Messages)-1].Extra[model.ExtraModel]; ok {
		t.Error("message tagged without a fallback chain")
	}
	// A plain outage ends the run with the provider's own error.
	a = newAgent(t, &failing{err: outage()})
	tr = a.Run(context.Background(), "q")
	if tr.ExitStatus != StatusError || tr.Submission != "giving up after 6 attempts: HTTP 503: overloaded" {
		t.Errorf("status %s: %q", tr.ExitStatus, tr.Submission)
	}
}
