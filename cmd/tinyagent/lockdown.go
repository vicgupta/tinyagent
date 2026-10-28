package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// lockdownFlags are the untrusted-content lockdown flags (#171).
type lockdownFlags struct {
	enabled       bool
	untrustedTask bool
}

func registerLockdownFlags(fs *flag.FlagSet, l *lockdownFlags) {
	fs.BoolVar(&l.enabled, "lockdown", false, "untrusted-content lockdown (overrides untrusted_content.enabled; --lockdown=false turns it off): once the run reads untrusted content (a web_fetch or MCP result, a review diff, --untrusted-task), commands and MCP tools are refused, and web_fetch outside untrusted_content.lockdown.web_fetch_allowed_domains")
	fs.BoolVar(&l.untrustedTask, "untrusted-task", false, "treat the task text as untrusted (an issue body, a ticket): the run starts tainted; requires lockdown (--lockdown or untrusted_content.enabled), it does not turn it on")
}

// errACPLockdown is returned when lockdown is asked for in --acp mode,
// which doesn't support it yet: it fails closed rather than running
// sessions unrestricted.
var errACPLockdown = errors.New("untrusted-content lockdown (--lockdown, --untrusted-task, untrusted_content.enabled) isn't supported in --acp mode yet")

// acpLockdownErr reports whether lockdown would be on for an ACP session
// with cfg.
func acpLockdownErr(fs *flag.FlagSet, l lockdownFlags, cfg config.AgentConfig) error {
	on := cfg.UntrustedContent.Enabled
	if flagWasSet(fs, "lockdown") {
		on = l.enabled
	}
	if on || l.untrustedTask {
		return errACPLockdown
	}
	return nil
}

// checkACPLockdown refuses --acp up front when the flags or the config ask
// for lockdown. A config that fails to load is left to the session, which
// reports it.
func checkACPLockdown(o *options, fs *flag.FlagSet) int {
	cfg, err := config.LoadProfile(config.Find(o.configPath), o.profile)
	if err != nil {
		cfg = config.AgentConfig{}
	}
	if err := acpLockdownErr(fs, o.lockdown, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return 0
}

// setupLockdown applies --lockdown to cfg and returns the run's lockdown
// state, nil when lockdown is off. A run starts tainted with
// --untrusted-task, in review mode (the diff is repository content), and
// on a resume whose checkpoint already holds untrusted content. A non-zero
// code is the exit code: --untrusted-task without lockdown is a usage
// error, since it doesn't turn lockdown on by itself.
func setupLockdown(fs *flag.FlagSet, l lockdownFlags, cfg *config.AgentConfig, review, resumeUntrusted bool, stderr io.Writer) (*lockdown.State, int) {
	if flagWasSet(fs, "lockdown") {
		cfg.UntrustedContent.Enabled = l.enabled
	}
	if !cfg.UntrustedContent.Enabled {
		if l.untrustedTask {
			fmt.Fprintln(stderr, "error: --untrusted-task requires lockdown: add --lockdown or set untrusted_content.enabled")
			return nil, 2
		}
		return nil, 0
	}
	st, err := lockdown.New(cfg.UntrustedContent.Lockdown.WebFetchAllowedDomains)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 2
	}
	if err := st.SetSources(cfg.UntrustedContent.Sources); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 2
	}
	st.SetMCPServers(mcpLockdownPolicies(cfg.MCPServers))
	if err := st.SetCommandAllowedDomains(cfg.UntrustedContent.Lockdown.AllowedDomains); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 2
	}
	if l.untrustedTask {
		st.Taint(lockdown.Source{Source: lockdown.SourceUntrustedTask})
	}
	if review {
		st.Taint(lockdown.Source{Source: lockdown.SourceReview})
	}
	if resumeUntrusted {
		st.Taint(lockdown.Source{Source: lockdown.SourceResume})
	}
	return st, 0
}

// startLockdown attaches st to a (and its web_fetch, whose redirect hops
// are then checked too), lets sandboxed commands keep running after taint
// (see confineLockdownCommands), prints the startup line, and emits the taint
// events of a run that starts tainted. It does nothing when st is nil.
func startLockdown(a *agent.Agent, st *lockdown.State, stderr io.Writer, events *runEvents) {
	if st == nil {
		return
	}
	a.Lockdown = st
	if a.Tools != nil && a.Tools.WebFetch != nil {
		a.Tools.WebFetch.Gate = st.WebFetchGate
	}
	if a.Tools != nil {
		confineLockdownCommands(st, tools.CommandSandbox(a.Tools.Env), stderr)
	}
	fmt.Fprintln(stderr, st.Describe())
	for _, s := range st.Sources() {
		if s.Resumed {
			fmt.Fprintf(stderr, "lockdown: run starts tainted (%s, from the checkpoint)\n", s.Source)
		} else {
			fmt.Fprintf(stderr, "lockdown: run starts tainted (%s)\n", s.Source)
		}
		events.emit("taint", s.Fields())
	}
}

// confineLockdownCommands lets commands keep running after taint when sb
// confines their network: denied, or through the allowlist proxy, which
// is narrowed at the first taint to the hosts in both
// sandbox_allowed_domains and untrusted_content.lockdown.allowed_domains,
// closing the connections opened before. Otherwise (no --sandbox, a
// container, sandbox_network) commands stay refused after taint, and it
// says so.
func confineLockdownCommands(st *lockdown.State, sb *tools.Sandbox, stderr io.Writer) {
	if !sb.LockdownConfinable() {
		fmt.Fprintln(stderr, "lockdown: commands will be refused after taint; run them under --sandbox without sandbox_network to keep them running with their network narrowed")
		return
	}
	if !sb.Proxied() {
		if len(st.CommandDomains()) > 0 {
			fmt.Fprintln(stderr, "lockdown: note: untrusted_content.lockdown.allowed_domains has no effect: sandboxed commands have no network (sandbox_allowed_domains is empty)")
		}
		st.ConfineCommands(false, nil)
		return
	}
	st.ConfineCommands(true, func(domains []string) {
		if err := sb.NarrowNetwork(domains); err != nil {
			// Validated at startup; never expected.
			fmt.Fprintln(stderr, "lockdown: error narrowing the sandbox network:", err)
		}
	})
}
