package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Unit tests for the Agent.Run phases split out in issue #416.

func TestPrepareRunDefaultsAndWallTime(t *testing.T) {
	a := newAgent(t, &scripted{})
	ctx, cancel := a.prepareRun(context.Background())
	defer cancel()
	if a.Out != io.Discard || a.Err != io.Discard {
		t.Errorf("writers not defaulted: out=%v err=%v", a.Out, a.Err)
	}
	if _, ok := ctx.Deadline(); ok {
		t.Error("deadline set without a wall-time limit")
	}
	a.Config.WallTimeSec = 60
	ctx, cancel2 := a.prepareRun(context.Background())
	defer cancel2()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("no deadline with a wall-time limit")
	}
}

func TestResumeSettingMismatchReportsFirstInOrder(t *testing.T) {
	a := newAgent(t, &scripted{})
	tr := &trajectory.Trajectory{}
	if msg := a.resumeSettingMismatch(tr); msg != "" {
		t.Fatalf("matching settings reported %q", msg)
	}
	tr.ImagesEnabled, tr.AskUserEnabled = true, true
	if msg := a.resumeSettingMismatch(tr); msg != "resume images setting differs from checkpoint" {
		t.Errorf("first mismatch = %q", msg)
	}
	tr.ImagesEnabled = false
	if msg := a.resumeSettingMismatch(tr); msg != "resume ask_user setting differs from checkpoint" {
		t.Errorf("remaining mismatch = %q", msg)
	}
}

func TestResumePreflightModelMismatch(t *testing.T) {
	a := newAgent(t, &scripted{})
	_, _, err := a.resumePreflight(&trajectory.Trajectory{Model: "other"})
	if err == nil || err.Error() != "resume model differs from checkpoint" {
		t.Errorf("err = %v", err)
	}
}

func TestAppendResumeNotice(t *testing.T) {
	a := newAgent(t, &scripted{})
	a.FollowUp = "now add tests"
	tr := &trajectory.Trajectory{}
	a.appendResumeNotice(tr, false, []string{"note A"})
	if got := tr.Messages[0].Content; got != resumeNotice+"\n\nnote A" {
		t.Errorf("continuation notice = %q", got)
	}
	a.appendResumeNotice(tr, true, nil)
	if got := tr.Messages[1].Content; got != trajectory.FollowUpNotice+"\n\nnow add tests" {
		t.Errorf("follow-up notice = %q", got)
	}
}

func TestCompactionReason(t *testing.T) {
	a := newAgent(t, &scripted{})
	tr := a.newTrajectory("task")
	tr.Messages = append(tr.Messages, model.Message{Role: "assistant", Content: "hi"})
	v := viewFromTrajectory(tr)
	if got := a.compactionReason(tr, v); got != "" {
		t.Errorf("small history reason = %q", got)
	}
	a.CompactFirst, a.NoCompact = true, true
	if got := a.compactionReason(tr, v); got != "" || !a.CompactFirst {
		t.Errorf("NoCompact: reason %q, CompactFirst %v", got, a.CompactFirst)
	}
	a.NoCompact = false
	if got := a.compactionReason(tr, v); got != "requested" || a.CompactFirst {
		t.Errorf("CompactFirst: reason %q, CompactFirst %v", got, a.CompactFirst)
	}
}

func TestRecordReplyAccounting(t *testing.T) {
	a := newAgent(t, &scripted{})
	a.Out, a.Err = io.Discard, io.Discard
	tr := a.newTrajectory("task")
	v := viewFromTrajectory(tr)
	msg := model.Message{Role: "assistant", Content: "thinking"}
	a.recordReply(tr, v, msg, model.Cost{USD: 0.5, InputTokens: 10, OutputTokens: 3})
	if tr.Steps != 1 || tr.Cost != 0.5 || tr.InputTokens != 10 || tr.OutputTokens != 3 {
		t.Errorf("accounting = steps %d cost %v in %d out %d", tr.Steps, tr.Cost, tr.InputTokens, tr.OutputTokens)
	}
	if last := tr.Messages[len(tr.Messages)-1]; last.Content != "thinking" {
		t.Errorf("reply not appended: %+v", last)
	}
	if tr.LastUsage == nil || tr.LastUsage.Messages != len(tr.Messages) || tr.LastUsage.Step != 1 {
		t.Errorf("LastUsage = %+v", tr.LastUsage)
	}
}

func TestFinishReply(t *testing.T) {
	a := newAgent(t, &scripted{})
	a.Out, a.Err = io.Discard, io.Discard
	tr := a.newTrajectory("task")
	v := viewFromTrajectory(tr)
	tools := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "list_files", `{}`)}}
	if done, handled := a.finishReply(context.Background(), tr, v, tools); handled || done != nil {
		t.Fatalf("tool-only reply handled: %v %v", done, handled)
	}
	submit := model.Message{Role: "assistant", Content: "all done\n" + SubmitMarker}
	done, handled := a.finishReply(context.Background(), tr, v, submit)
	if !handled || done == nil || done.ExitStatus != StatusSubmitted || done.Submission != "all done" {
		t.Errorf("submit: handled %v trajectory %+v", handled, done)
	}
}

func TestCountFormatErrors(t *testing.T) {
	a := newAgent(t, &scripted{})
	a.Config.MaxFormatErrors = 2
	tr := &trajectory.Trajectory{}
	if done := a.countFormatErrors(tr, 1, 2); done != nil || tr.ConsecutiveFormatErrors != 0 {
		t.Fatalf("partial batch: %v, %d", done, tr.ConsecutiveFormatErrors)
	}
	if done := a.countFormatErrors(tr, 2, 2); done != nil || tr.ConsecutiveFormatErrors != 1 {
		t.Fatalf("first malformed batch: %v, %d", done, tr.ConsecutiveFormatErrors)
	}
	if done := a.countFormatErrors(tr, 1, 1); done == nil || done.ExitStatus != StatusFormatError {
		t.Errorf("cap reached: %+v", done)
	}
}

func TestExecuteCallFormatError(t *testing.T) {
	a := newAgent(t, &scripted{})
	tr := a.newTrajectory("task")
	o, done := a.executeCall(context.Background(), tr, call("1", "read_file", `{not json`), nil)
	if done != nil {
		t.Fatalf("run ended: %+v", done)
	}
	if !o.formatError || !o.isError || tr.FormatErrors != 1 || tr.LastFormatError == "" {
		t.Errorf("outcome %+v, FormatErrors %d", o, tr.FormatErrors)
	}
}

type failingSnapshots struct{}

func (failingSnapshots) Take(string, int) (trajectory.Snapshot, error) {
	return trajectory.Snapshot{}, errors.New("disk full")
}

func TestStepSnapshotFailureGivesEveryCallAResult(t *testing.T) {
	a := newAgent(t, &scripted{})
	a.Out, a.Err = io.Discard, io.Discard
	a.Snapshots = failingSnapshots{}
	tr := a.newTrajectory("task")
	v := viewFromTrajectory(tr)
	msg := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{
		call("1", "write_file", `{"path":"a","content":"x"}`),
		call("2", "bash", `{"command":"true"}`),
	}}
	tr.Messages = append(tr.Messages, msg)
	done := a.stepSnapshot(tr, v, msg)
	if done == nil || done.ExitStatus != StatusError || !strings.Contains(done.Submission, "disk full") {
		t.Fatalf("trajectory = %+v", done)
	}
	results := tr.Messages[len(tr.Messages)-2:]
	for i, id := range []string{"1", "2"} {
		if results[i].Role != "tool" || results[i].ToolCallID != id || !strings.HasPrefix(results[i].Content, "error: not run: ") {
			t.Errorf("result %d = %+v", i, results[i])
		}
	}
	// A read-only batch takes no snapshot, so the failing snapshotter is
	// never reached.
	ro := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("3", "read_file", `{"path":"a"}`)}}
	if done := a.stepSnapshot(tr, v, ro); done != nil {
		t.Errorf("read-only batch: %+v", done)
	}
}

func TestRunSpecsAddsSubmitToolOnlyWithSchema(t *testing.T) {
	a := newAgent(t, &scripted{})
	for _, s := range a.runSpecs() {
		if s.Function.Name == SubmitToolName {
			t.Fatalf("%s offered without a schema", SubmitToolName)
		}
	}
}
