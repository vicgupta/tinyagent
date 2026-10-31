package eval

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestUsageByRoleMarkdown(t *testing.T) {
	if got := usageByRoleMarkdown(Report{Configs: []ConfigReport{{Model: "a/b"}}}); got != "" {
		t.Errorf("no usage = %q", got)
	}
	list := addRoleUsage(nil, trajectory.RoleUsage{Role: "delegated", Task: "worker", Model: "a/c", Requests: 1, InputTokens: 5})
	list = addRoleUsage(list, trajectory.RoleUsage{Role: "delegated", Task: "worker", Model: "a/c", Requests: 2, InputTokens: 5})
	got := usageByRoleMarkdown(Report{Configs: []ConfigReport{{Model: "a/b", UsageByRole: list}}})
	if !strings.Contains(got, "## Usage by role") || !strings.Contains(got, "| a/b |  | delegated | worker | a/c | 3 | 10 |") {
		t.Errorf("markdown = %q", got)
	}
}
