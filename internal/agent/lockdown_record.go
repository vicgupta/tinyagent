package agent

import (
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// recordLockdown copies the run's lockdown state into t at every step, so
// checkpoints and the final trajectory carry it. Without lockdown it only
// clears LockdownEnabled: a taint recorded by an earlier invocation stays.
func (a *Agent) recordLockdown(t *trajectory.Trajectory) {
	if a.Lockdown == nil {
		t.LockdownEnabled = false
		return
	}
	t.LockdownEnabled = true
	sources := a.Lockdown.Sources()
	if len(sources) > 0 {
		t.Tainted = true
	}
	t.TaintSources = nil
	for _, s := range sources {
		t.TaintSources = append(t.TaintSources, trajectory.TaintSource{
			Source: s.Source, Step: s.Step, ToolCallID: s.ToolCallID, Tool: s.Tool, Host: s.Host, Resumed: s.Resumed,
		})
	}
	t.LockdownDenials = nil
	for _, d := range a.Lockdown.Denials() {
		t.LockdownDenials = append(t.LockdownDenials, trajectory.LockdownDenial{
			Channel: d.Channel, Tool: d.Tool, ToolCallID: d.ToolCallID, Step: d.Step, Host: d.Host,
		})
	}
}

// ResumeLockdown returns the taint sources and lockdown denials a
// checkpoint recorded, for lockdown.State.Restore.
func ResumeLockdown(t *trajectory.Trajectory) ([]lockdown.Source, []lockdown.Denial) {
	var sources []lockdown.Source
	for _, s := range t.TaintSources {
		sources = append(sources, lockdown.Source{
			Source: s.Source, Step: s.Step, ToolCallID: s.ToolCallID, Tool: s.Tool, Host: s.Host, Resumed: s.Resumed,
		})
	}
	var denials []lockdown.Denial
	for _, d := range t.LockdownDenials {
		denials = append(denials, lockdown.Denial{
			Channel: d.Channel, Tool: d.Tool, ToolCallID: d.ToolCallID, Step: d.Step, Host: d.Host,
		})
	}
	return sources, denials
}
