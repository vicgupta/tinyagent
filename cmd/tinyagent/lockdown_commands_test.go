package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

func TestConfineLockdownCommands(t *testing.T) {
	for _, c := range []struct {
		name     string
		sb       *tools.Sandbox
		domains  []string
		confined bool
		stderr   string
	}{
		{"unsandboxed", nil, nil, false, "commands will be refused after taint"},
		{"sandbox_network", &tools.Sandbox{Network: true}, nil, false, "commands will be refused after taint"},
		{"network denied", &tools.Sandbox{}, nil, true, ""},
		{"network denied with lockdown domains", &tools.Sandbox{}, []string{"proxy.golang.org"}, true, "allowed_domains has no effect"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, _ := lockdown.New(nil)
			if err := st.SetCommandAllowedDomains(c.domains); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			confineLockdownCommands(st, c.sb, &buf)
			if st.CommandsConfined() != c.confined {
				t.Errorf("confined = %v", st.CommandsConfined())
			}
			if c.stderr == "" && buf.Len() != 0 || !strings.Contains(buf.String(), c.stderr) {
				t.Errorf("stderr %q, want %q", buf.String(), c.stderr)
			}
		})
	}
}
