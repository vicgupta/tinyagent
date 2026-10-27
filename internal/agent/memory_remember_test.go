package agent

import (
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestRememberOffered(t *testing.T) {
	with := []string{"read_file", tools.RememberToolName}
	for _, c := range []struct {
		name string
		tr   trajectory.Trajectory
		want bool
	}{
		{"offered", trajectory.Trajectory{MemoryEnabled: true, Tools: with}, true},
		{"memory off", trajectory.Trajectory{Tools: with}, false},
		{"read-only", trajectory.Trajectory{MemoryEnabled: true, ReadOnly: true, Tools: with}, false},
		{"older checkpoint", trajectory.Trajectory{MemoryEnabled: true, Tools: []string{"read_file"}}, false},
	} {
		if got := RememberOffered(&c.tr); got != c.want {
			t.Errorf("%s: RememberOffered = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestResumeUntrusted(t *testing.T) {
	msg := func(name string) model.Message {
		return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "1", Function: model.FunctionCall{Name: name}}}}
	}
	for _, c := range []struct {
		name string
		tr   trajectory.Trajectory
		want bool
	}{
		{"builtin only", trajectory.Trajectory{Messages: []model.Message{msg("read_file")}}, false},
		{"web fetch", trajectory.Trajectory{Messages: []model.Message{msg(tools.WebFetchToolName)}}, true},
		{"external tool", trajectory.Trajectory{ExternalTools: []string{"mcp_docs_search"}, Messages: []model.Message{msg("mcp_docs_search")}}, true},
		{"external offered, not called", trajectory.Trajectory{ExternalTools: []string{"mcp_docs_search"}, Messages: []model.Message{msg("read_file")}}, false},
		{"review", trajectory.Trajectory{Review: &trajectory.ReviewInfo{}}, true},
	} {
		if got := ResumeUntrusted(&c.tr); got != c.want {
			t.Errorf("%s: ResumeUntrusted = %v, want %v", c.name, got, c.want)
		}
	}
}
