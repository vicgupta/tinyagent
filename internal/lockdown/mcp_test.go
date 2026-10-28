package lockdown

import (
	"strings"
	"testing"
)

func TestMCPServerPolicyDefaultsFailClosed(t *testing.T) {
	st, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !st.MCPCallTaints([]string{"docs"}) || st.MCPCallAllowed([]string{"docs"}) {
		t.Error("an unconfigured server must taint and be refused")
	}
	st.SetMCPServers([]MCPServerPolicy{
		{Alias: "docs", Trusted: true, LockdownAllowed: true},
		{Alias: "issues"},
		{Alias: "search", LockdownAllowed: true},
	})
	cases := []struct {
		aliases        []string
		taints, allows bool
	}{
		{nil, true, false},
		{[]string{"docs"}, false, true},
		{[]string{"issues"}, true, false},
		{[]string{"search"}, true, true},
		{[]string{"unknown"}, true, false},
		{[]string{"docs", "issues"}, true, false},
		{[]string{"docs", "search"}, true, true},
	}
	for _, c := range cases {
		if got := st.MCPCallTaints(c.aliases); got != c.taints {
			t.Errorf("MCPCallTaints(%v) = %v", c.aliases, got)
		}
		if got := st.MCPCallAllowed(c.aliases); got != c.allows {
			t.Errorf("MCPCallAllowed(%v) = %v", c.aliases, got)
		}
	}
	if got := strings.Join(st.MCPAliases(), ","); got != "docs,issues,search" {
		t.Errorf("aliases = %q", got)
	}
	d := st.Describe()
	if !strings.Contains(d, "mcp → docs, search") || !strings.Contains(d, "mcp results that don't taint: docs") {
		t.Errorf("describe = %q", d)
	}
}

func TestMCPServerPolicyNilState(t *testing.T) {
	var st *State
	st.SetMCPServers([]MCPServerPolicy{{Alias: "x"}})
	if st.MCPCallTaints([]string{"x"}) || !st.MCPCallAllowed(nil) || st.MCPAliases() != nil {
		t.Error("a nil state (lockdown off) must neither taint nor refuse")
	}
}

func TestDescribeWithoutMCPPolicyUnchanged(t *testing.T) {
	st, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := st.Describe(); !strings.HasSuffix(d, "mcp tools refused)") {
		t.Errorf("describe = %q", d)
	}
}
