package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/snapshot"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func kinds(snaps []trajectory.Snapshot) []string {
	var out []string
	for _, s := range snaps {
		out = append(out, s.Kind+"@"+strconv.Itoa(s.Step))
	}
	return out
}

func TestSnapshotsAtBaselineMutatingStepsAndEnd(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "read_file", `{"path":"f.txt"}`)}},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("2", "edit_file", `{"path":"f.txt","old":"hi","new":"bye"}`)}},
		{Role: "assistant", Content: "Done.\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}
	a := newAgent(t, m)
	os.WriteFile(filepath.Join(a.Tools.Root, "f.txt"), []byte("hi"), 0o644)
	s, err := snapshot.Open(a.Tools.Root, snapshot.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = s
	var events []string
	a.OnEvent = func(kind string, f map[string]any) {
		if kind == "workspace.snapshot" {
			events = append(events, f["kind"].(string))
			for _, k := range []string{"id", "step", "time", "files", "bytes", "skipped_ignored", "skipped_large", "store", "duration_ms"} {
				if _, ok := f[k]; !ok {
					t.Errorf("event missing %s: %v", k, f)
				}
			}
		}
	}
	tr := a.Run(context.Background(), "edit")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s: %s", tr.ExitStatus, tr.Submission)
	}
	if got, want := kinds(tr.Snapshots), []string{"baseline@0", "step@2", "final@3"}; !slices.Equal(got, want) {
		t.Errorf("snapshots = %v, want %v", got, want)
	}
	if !tr.SnapshotsEnabled {
		t.Error("snapshots_enabled not recorded")
	}
	if len(events) != 3 {
		t.Errorf("events = %v", events)
	}
}

func TestNoSnapshotsByDefault(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
		{Role: "assistant", Content: "Done.\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}
	a := newAgent(t, m)
	a.OnEvent = func(kind string, _ map[string]any) {
		if kind == "workspace.snapshot" {
			t.Error("snapshot event with snapshots off")
		}
	}
	tr := a.Run(context.Background(), "write")
	if tr.SnapshotsEnabled || len(tr.Snapshots) != 0 {
		t.Errorf("trajectory has snapshot data: %+v", tr.Snapshots)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, ".tinyagent")); err == nil {
		t.Error("snapshot store created with snapshots off")
	}
}

type failingSnaps struct {
	inner  Snapshotter
	failAt int
	calls  int
	kinds  []string
}

func (f *failingSnaps) Take(kind string, step int) (trajectory.Snapshot, error) {
	f.calls++
	f.kinds = append(f.kinds, kind)
	if f.calls == f.failAt {
		return trajectory.Snapshot{}, errors.New("disk full")
	}
	return f.inner.Take(kind, step)
}

func TestFailedStepSnapshotSkipsTools(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
	}}
	a := newAgent(t, m)
	s, err := snapshot.Open(a.Tools.Root, snapshot.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fs := &failingSnaps{inner: s, failAt: 2}
	a.Snapshots = fs
	tr := a.Run(context.Background(), "write")
	if tr.ExitStatus != StatusError {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "f.txt")); err == nil {
		t.Error("tool ran after its snapshot failed")
	}
	if got := kinds(tr.Snapshots); !slices.Equal(got, []string{"baseline@0"}) {
		t.Errorf("snapshots = %v (no final after a failed store)", got)
	}
}

func TestFailedBaselineMakesNoRequest(t *testing.T) {
	m := &scripted{}
	a := newAgent(t, m)
	a.Snapshots = &failingSnaps{failAt: 1}
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusError || len(m.seen) != 0 {
		t.Errorf("status = %s, requests = %d", tr.ExitStatus, len(m.seen))
	}
}
