package tools

import (
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

func TestUntrustedSource(t *testing.T) {
	ext := []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "mcp__docs__search"}}}
	cases := []struct {
		name string
		e    *Executor
		tool string
		want string
	}{
		{"nil executor", nil, WebFetchToolName, ""},
		{"web_fetch enabled", &Executor{WebFetch: &WebFetcher{}}, WebFetchToolName, UntrustedWebFetch},
		{"web_fetch disabled", &Executor{}, WebFetchToolName, ""},
		{"external tool", &Executor{ExternalSpecs: ext}, "mcp__docs__search", UntrustedMCP},
		{"unknown external", &Executor{ExternalSpecs: ext}, "mcp__docs__other", ""},
		{"built-in", &Executor{WebFetch: &WebFetcher{}, ExternalSpecs: ext}, "read_file", ""},
		{"bash", &Executor{WebFetch: &WebFetcher{}, ExternalSpecs: ext}, "bash", ""},
	}
	for _, c := range cases {
		if got := c.e.UntrustedSource(c.tool); got != c.want {
			t.Errorf("%s: UntrustedSource(%q) = %q, want %q", c.name, c.tool, got, c.want)
		}
	}
}

func TestUntrustedHistoryCall(t *testing.T) {
	ext := []string{"mcp__docs__search"}
	for tool, want := range map[string]bool{
		WebFetchToolName:    true,
		"mcp__docs__search": true,
		"mcp__docs__other":  false,
		"read_file":         false,
		"bash":              false,
	} {
		if got := UntrustedHistoryCall(tool, ext); got != want {
			t.Errorf("UntrustedHistoryCall(%q) = %v, want %v", tool, got, want)
		}
	}
	if UntrustedHistoryCall("mcp__docs__search", nil) {
		t.Error("a tool not recorded as external must not count")
	}
}
