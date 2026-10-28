package lockdown

import (
	"slices"
	"testing"
)

func TestMCPTaintSource(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetMCPServers([]MCPServerPolicy{{Alias: "docs", Trusted: true}, {Alias: "web"}})
	s.SetACPClientMCPServers([]string{"editor"})
	if got := s.MCPAliases(); !slices.Equal(got, []string{"docs", "editor", "web"}) {
		t.Errorf("aliases = %v", got)
	}
	for _, tc := range []struct {
		aliases []string
		src     string
		taints  bool
	}{
		{[]string{"docs"}, "", false},
		{[]string{"web"}, SourceMCP, true},
		{[]string{"editor"}, SourceACPClientMCP, true},
		{[]string{"docs", "editor"}, SourceACPClientMCP, true},
		{[]string{"unknown"}, SourceMCP, true},
		{nil, SourceMCP, true},
	} {
		src, taints := s.MCPTaintSource(tc.aliases)
		if src != tc.src || taints != tc.taints {
			t.Errorf("%v: %q %v", tc.aliases, src, taints)
		}
	}
	if s.MCPCallAllowed([]string{"editor"}) {
		t.Error("client server allowed after taint without a policy")
	}

	// With mcp turned off, only client servers taint, and an unattributed
	// call falls back to acp_client_mcp.
	if err := s.SetSources([]string{SourceACPClientMCP}); err != nil {
		t.Fatal(err)
	}
	if _, taints := s.MCPTaintSource([]string{"web"}); taints {
		t.Error("mcp source taints while disabled")
	}
	if src, taints := s.MCPTaintSource(nil); src != SourceACPClientMCP || !taints {
		t.Errorf("unattributed call: %q %v", src, taints)
	}
	if err := s.SetSources([]string{SourceWebFetch}); err != nil {
		t.Fatal(err)
	}
	if _, taints := s.MCPTaintSource([]string{"editor"}); taints {
		t.Error("acp_client_mcp taints while disabled")
	}
	var nilState *State
	if _, taints := nilState.MCPTaintSource(nil); taints {
		t.Error("nil state taints")
	}
}
