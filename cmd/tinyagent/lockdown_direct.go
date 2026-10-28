package main

import (
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// confineDirectLockdownCommands keeps sandbox_network commands running
// after taint (#171): the first taint takes their direct network away, so
// later commands have their network denied, or reach only
// untrusted_content.lockdown.allowed_domains through a proxy, and the
// background processes started before are stopped. When this host can't
// enforce that, commands stay refused after taint, and it says why.
func confineDirectLockdownCommands(st *lockdown.State, sb *tools.Sandbox, stderr io.Writer) {
	domains := st.CommandDomains()
	if err := sb.PrepareLockdownNetwork(domains); err != nil {
		fmt.Fprintf(stderr, "lockdown: commands will be refused after taint: sandbox_network can't be taken away at taint here (%v)\n", err)
		return
	}
	st.ConfineCommands(sb.Proxied(), func(domains []string) {
		if err := sb.NarrowNetwork(domains); err != nil {
			// Prepared at startup; never expected.
			fmt.Fprintln(stderr, "lockdown: error taking away the sandbox network:", err)
		}
	})
	st.SetCommandsDirectUntilTaint()
	fmt.Fprintln(stderr, "lockdown: sandbox_network: commands keep direct network until taint; after it they run with "+directAfterTaint(len(domains))+", and background processes started before are stopped")
}

// directAfterTaint names the network sandbox_network commands get after
// taint.
func directAfterTaint(n int) string {
	switch n {
	case 0:
		return "network denied"
	case 1:
		return "network limited to 1 untrusted_content.lockdown.allowed_domains entry"
	}
	return fmt.Sprintf("network limited to %d untrusted_content.lockdown.allowed_domains entries", n)
}
