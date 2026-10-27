package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func patchArgs(patch string) string {
	b, _ := json.Marshal(map[string]string{"input": patch})
	return string(b)
}

const addPatch = "*** Begin Patch\n*** Add File: new.txt\n+hi\n*** End Patch"

func TestApplyPatchOffKeepsSignature(t *testing.T) { // AC1
	for _, ro := range []bool{false, true} {
		for _, plan := range []bool{false, true} {
			for _, img := range []bool{false, true} {
				if ApplyPatchToolSignature(ro, plan, img, false) != ImagesToolSignature(ro, plan, img) {
					t.Errorf("signature changed with apply_patch off (read-only %v, plan %v, images %v)", ro, plan, img)
				}
				if ApplyPatchToolSignature(ro, plan, img, true) == ImagesToolSignature(ro, plan, img) {
					t.Errorf("apply_patch did not change the signature")
				}
			}
		}
	}
	if ApplyPatchToolSignature(false, false, false, false) != ToolSignature() {
		t.Error("default signature changed")
	}
}

func TestApplyPatchRecordedAndResumeMustMatch(t *testing.T) { // AC13
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "apply_patch", patchArgs(addPatch))}},
		submit,
	}}
	a := newAgent(t, m)
	a.Tools.ApplyPatch = true
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted || !tr.ApplyPatchEnabled || !strings.HasPrefix(tr.ToolSignature, "apply-patch-v1:") {
		t.Fatalf("status %s, enabled %v, signature %s", tr.ExitStatus, tr.ApplyPatchEnabled, tr.ToolSignature)
	}
	if res := toolResults(tr.Messages); len(res) != 1 || res[0] != "ok\nA new.txt" {
		t.Fatalf("results = %q", res)
	}
	if tr.Tools[len(tr.Tools)-1] != "apply_patch" {
		t.Errorf("tools = %v", tr.Tools)
	}
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
		t.Errorf("ValidateResume: %v", err)
	}
	a.Tools.ApplyPatch = false
	a.Resume, a.Model, a.FollowUp = tr, &scripted{replies: []model.Message{submit}}, "more"
	if r := a.Run(context.Background(), "more"); r.ExitStatus != StatusError || !strings.Contains(r.Submission, "apply_patch setting differs") {
		t.Errorf("mismatched resume: %s %q", r.ExitStatus, r.Submission)
	}
	tampered := *tr
	tampered.ApplyPatchEnabled = false
	if err := ValidateResume(&tampered, a.Config, a.Tools.Root); err == nil {
		t.Error("ValidateResume accepted a changed apply_patch setting")
	}
	v1 := trajectory.Trajectory{SchemaVersion: 1, ApplyPatchEnabled: true}
	if err := v1.Validate(); err == nil || !strings.Contains(err.Error(), "version 2 fields") {
		t.Errorf("schema 1 with apply_patch: %v", err)
	}
}

func TestApplyPatchHooks(t *testing.T) { // AC11
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "apply_patch", patchArgs(addPatch))}},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("2", "apply_patch", patchArgs(addPatch))}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Tools.ApplyPatch = true
	withHooks(t, a, config.Hooks{
		PreTool:  []config.Hook{hookstest.Hook(t, "apply_patch", "exit2", "no patches")},
		PostTool: []config.Hook{hookstest.Hook(t, "write_file|edit_file|apply_patch", "stdout", `{"feedback":"checked"}`)},
	})
	var kinds []string
	a.OnEvent = func(kind string, _ map[string]any) { kinds = append(kinds, kind) }
	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.HasPrefix(res[0], "error: denied by pre_tool hook ") || !strings.HasSuffix(res[0], ": no patches") {
		t.Fatalf("results = %q", res)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "new.txt")); err == nil {
		t.Error("denied patch was applied")
	}
	if !strings.HasPrefix(strings.Join(kinds, ","), "model.request,tool.call,hook,tool.result,") {
		t.Errorf("events = %v", kinds)
	}

	m2 := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "apply_patch", patchArgs(addPatch))}},
		{Role: "assistant", Content: "ok"},
	}}
	a2 := newAgent(t, m2)
	a2.Tools.ApplyPatch = true
	withHooks(t, a2, config.Hooks{PostTool: []config.Hook{hookstest.Hook(t, "write_file|edit_file|apply_patch", "stdout", `{"feedback":"checked"}`)}})
	kinds = nil
	a2.OnEvent = func(kind string, _ map[string]any) { kinds = append(kinds, kind) }
	tr = a2.Run(context.Background(), "task")
	if res := toolResults(tr.Messages); len(res) != 1 || !strings.HasPrefix(res[0], "ok\nA new.txt\n[post_tool hook ") || !strings.HasSuffix(res[0], "]\nchecked") {
		t.Fatalf("results = %q", res)
	}
	if !strings.HasPrefix(strings.Join(kinds, ","), "model.request,tool.call,hook,tool.result,") {
		t.Errorf("events = %v", kinds)
	}
}

func TestApplyPatchMissingInputIsFormatError(t *testing.T) { // AC14
	var results []map[string]any
	tr, _ := runSteps(t, func(a *Agent) {
		a.Tools.ApplyPatch = true
		a.OnEvent = func(kind string, f map[string]any) {
			if kind == "tool.result" {
				results = append(results, f)
			}
		}
	}, toolStep(call("1", "apply_patch", `{}`)), toolStep(call("2", "apply_patch", `{"input":5}`)),
		toolStep(call("3", "apply_patch", patchArgs("*** Begin Patch\n*** End Patch"))))
	if tr.FormatErrors != 2 || tr.ConsecutiveFormatErrors != 0 {
		t.Fatalf("format errors %d, consecutive %d", tr.FormatErrors, tr.ConsecutiveFormatErrors)
	}
	if len(results) != 3 || results[0]["format_error"] != true || results[1]["format_error"] != true ||
		results[2]["format_error"] != nil || results[2]["is_error"] != true {
		t.Errorf("results = %v", results)
	}
}
