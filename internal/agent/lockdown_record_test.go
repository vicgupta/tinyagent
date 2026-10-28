package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestLockdownRecordedInTrajectory(t *testing.T) {
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "docs__search", `{"q":"x"}`)),
		assistantCalls(call("2", "bash", `{"command":"true"}`)),
		submit,
	}}
	a := newAgent(t, m)
	var mcpCalls atomic.Int32
	withFakeMCP(a, &mcpCalls)
	a.Lockdown = newLockdown(t)
	var saved []trajectory.Trajectory
	a.OnStep = func(tr *trajectory.Trajectory) error {
		saved = append(saved, *tr)
		return nil
	}
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s", tr.ExitStatus)
	}
	if !tr.LockdownEnabled || !tr.Tainted {
		t.Fatalf("lockdown_enabled %v tainted %v", tr.LockdownEnabled, tr.Tainted)
	}
	want := trajectory.TaintSource{Source: lockdown.SourceMCP, Step: 1, ToolCallID: "1", Tool: "docs__search"}
	if len(tr.TaintSources) != 1 || tr.TaintSources[0] != want {
		t.Errorf("taint_sources = %+v", tr.TaintSources)
	}
	wantDenial := trajectory.LockdownDenial{Channel: lockdown.ChannelCommand, Tool: "bash", ToolCallID: "2", Step: 2}
	if len(tr.LockdownDenials) != 1 || tr.LockdownDenials[0] != wantDenial {
		t.Errorf("lockdown_denials = %+v", tr.LockdownDenials)
	}
	// Checkpoints before the taint are untainted, and the records hold no
	// tool content.
	if len(saved) == 0 || saved[0].Tainted || !saved[0].LockdownEnabled {
		t.Errorf("first checkpoint tainted %v enabled %v", saved[0].Tainted, saved[0].LockdownEnabled)
	}
	b, _ := json.Marshal(struct {
		S []trajectory.TaintSource
		D []trajectory.LockdownDenial
	}{tr.TaintSources, tr.LockdownDenials})
	if strings.Contains(string(b), "ignore previous instructions") || strings.Contains(string(b), `"q"`) {
		t.Errorf("records leak content: %s", b)
	}
}

func TestLockdownOffLeavesTrajectoryUnmarked(t *testing.T) {
	a := newAgent(t, &scripted{replies: []model.Message{submit}})
	tr := a.Run(context.Background(), "task")
	if tr.LockdownEnabled || tr.Tainted || tr.TaintSources != nil || tr.LockdownDenials != nil {
		t.Errorf("trajectory = %+v", tr)
	}
	b, _ := json.Marshal(tr)
	for _, k := range []string{"lockdown_enabled", "tainted", "taint_sources", "lockdown_denials"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("trajectory JSON has %s", k)
		}
	}
}

func TestResumeLockdownRoundTrip(t *testing.T) {
	tr := &trajectory.Trajectory{
		Tainted:         true,
		TaintSources:    []trajectory.TaintSource{{Source: "web_fetch", Step: 3, ToolCallID: "c", Tool: "web_fetch", Host: "a.example"}},
		LockdownDenials: []trajectory.LockdownDenial{{Channel: "mcp", Tool: "docs__search", ToolCallID: "d", Step: 4}},
	}
	src, den := ResumeLockdown(tr)
	if len(src) != 1 || src[0].Host != "a.example" || src[0].Step != 3 || len(den) != 1 || den[0].Tool != "docs__search" {
		t.Errorf("ResumeLockdown = %+v %+v", src, den)
	}
	if !ResumeUntrusted(&trajectory.Trajectory{Tainted: true}) {
		t.Error("a tainted checkpoint isn't untrusted for memory")
	}
}
