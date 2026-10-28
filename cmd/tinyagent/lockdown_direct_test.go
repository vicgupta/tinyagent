package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// Whether sandbox_network can be taken away at taint depends on the host
// (Landlock network rules, network namespaces); either way the outcome and
// the startup message must agree.
func TestConfineDirectLockdownCommands(t *testing.T) {
	st, _ := lockdown.New(nil)
	sb := &tools.Sandbox{Network: true}
	defer sb.Cleanup()
	var buf bytes.Buffer
	confineLockdownCommands(st, sb, &buf)
	if st.CommandsConfined() {
		if !sb.DirectUntilTaint() || !st.StopsProcessesAtTaint() {
			t.Error("confined without taking the direct network away at taint")
		}
		if !strings.Contains(buf.String(), "commands keep direct network until taint; after it they run with network denied") {
			t.Errorf("stderr %q", buf.String())
		}
	} else if !strings.Contains(buf.String(), "commands will be refused after taint: sandbox_network can't be taken away at taint here") {
		t.Errorf("stderr %q", buf.String())
	}
}

func TestDirectAfterTaint(t *testing.T) {
	for n, want := range map[int]string{
		0: "network denied",
		1: "network limited to 1 untrusted_content.lockdown.allowed_domains entry",
		3: "network limited to 3 untrusted_content.lockdown.allowed_domains entries",
	} {
		if got := directAfterTaint(n); got != want {
			t.Errorf("directAfterTaint(%d) = %q", n, got)
		}
	}
}
