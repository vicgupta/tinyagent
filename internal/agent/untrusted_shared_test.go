package agent

import (
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// The shared untrusted-source names must stay the lockdown source names,
// so memory (#170) and lockdown (#171) agree on what is untrusted.
func TestUntrustedSourceNamesMatchLockdown(t *testing.T) {
	if tools.UntrustedWebFetch != lockdown.SourceWebFetch {
		t.Errorf("web_fetch: tools %q, lockdown %q", tools.UntrustedWebFetch, lockdown.SourceWebFetch)
	}
	if tools.UntrustedMCP != lockdown.SourceMCP {
		t.Errorf("mcp: tools %q, lockdown %q", tools.UntrustedMCP, lockdown.SourceMCP)
	}
}

// ResumeUntrusted and the live rule agree: a checkpoint is untrusted when
// it recorded a call that UntrustedSource would flag.
func TestResumeUntrustedUsesSharedRule(t *testing.T) {
	call := func(name string) model.Message {
		return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "1", Function: model.FunctionCall{Name: name}}}}
	}
	cases := []struct {
		name string
		t    *trajectory.Trajectory
		want bool
	}{
		{"web_fetch call", &trajectory.Trajectory{Messages: []model.Message{call(tools.WebFetchToolName)}}, true},
		{"external call", &trajectory.Trajectory{ExternalTools: []string{"mcp__x__y"}, Messages: []model.Message{call("mcp__x__y")}}, true},
		{"external offered but not called", &trajectory.Trajectory{ExternalTools: []string{"mcp__x__y"}, Messages: []model.Message{call("read_file")}}, false},
		{"tainted", &trajectory.Trajectory{Tainted: true}, true},
		{"clean", &trajectory.Trajectory{Messages: []model.Message{call("bash")}}, false},
	}
	for _, c := range cases {
		if got := ResumeUntrusted(c.t); got != c.want {
			t.Errorf("%s: ResumeUntrusted = %v, want %v", c.name, got, c.want)
		}
	}
}
