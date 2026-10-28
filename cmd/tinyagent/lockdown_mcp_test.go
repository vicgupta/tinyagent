package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestSetupLockdownMCPServerPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `untrusted_content:
  enabled: true
mcp_servers:
  - alias: docs
    command: docs-server
    tools: [search]
    untrusted: false
    lockdown_allowed: true
  - alias: issues
    command: issues-server
    tools: [read]
  - alias: tracker
    command: tracker-server
    tools: [read]
    untrusted: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadProfile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	o := &options{}
	fs := newFlagSet(o)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	st, code := setupLockdown(fs, o.lockdown, &cfg, false, false, io.Discard)
	if code != 0 || st == nil {
		t.Fatalf("code %d", code)
	}
	if st.MCPCallTaints([]string{"docs"}) || !st.MCPCallAllowed([]string{"docs"}) {
		t.Error("docs: untrusted: false and lockdown_allowed: true not applied")
	}
	for _, alias := range []string{"issues", "tracker"} {
		if !st.MCPCallTaints([]string{alias}) || st.MCPCallAllowed([]string{alias}) {
			t.Errorf("%s: defaults not applied", alias)
		}
	}
}

func TestMCPLockdownKeysRejectWrongType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "mcp_servers:\n  - alias: docs\n    command: docs-server\n    tools: [search]\n    lockdown_allowed: maybe\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadProfile(path, ""); err == nil {
		t.Error("lockdown_allowed: maybe was accepted")
	}
}
