package main

import (
	"errors"
	"flag"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
)

// checkLockdown returns the --check lockdown line (issue #171), or "" when
// lockdown is off and --lockdown wasn't passed. It builds the lockdown
// state the way a run would, so it reports the same sources, web_fetch
// allowlist and MCP server policies a run prints at startup, and returns
// the error a run would stop on (exit 2): --untrusted-task without
// lockdown, or an invalid allowlist or source list.
func checkLockdown(fs *flag.FlagSet, l lockdownFlags, cfg config.AgentConfig) (string, error) {
	on := cfg.UntrustedContent.Enabled
	src := "from config"
	if flagWasSet(fs, "lockdown") {
		on = l.enabled
		src = "from --lockdown"
	}
	if !on {
		if l.untrustedTask {
			return "", errors.New("--untrusted-task requires lockdown: add --lockdown or set untrusted_content.enabled")
		}
		if src == "from --lockdown" {
			return "off (from --lockdown)", nil
		}
		return "", nil
	}
	st, err := lockdown.New(cfg.UntrustedContent.Lockdown.WebFetchAllowedDomains)
	if err != nil {
		return "", err
	}
	if err := st.SetSources(cfg.UntrustedContent.Sources); err != nil {
		return "", err
	}
	st.SetMCPServers(mcpLockdownPolicies(cfg.MCPServers))
	if err := st.SetCommandAllowedDomains(cfg.UntrustedContent.Lockdown.AllowedDomains); err != nil {
		return "", err
	}
	// Mirrors confineLockdownCommands: commands keep running after taint
	// only under the local sandbox. With sandbox_network they lose their
	// direct network at taint (if the host can deny it; the run says so at
	// startup otherwise).
	containerNote := ""
	if cfg.Environment == "container" && !cfg.ReadOnly {
		// The container's networks are disconnected at taint if its
		// network mode allows (checked from container.run_args).
		containerNote = checkContainerLockdown(st, cfg.Container)
	} else if cfg.Sandbox && cfg.Environment != "container" {
		if cfg.SandboxNetwork {
			st.ConfineCommands(len(cfg.UntrustedContent.Lockdown.AllowedDomains) > 0, nil)
			st.SetCommandsDirectUntilTaint()
		} else {
			st.ConfineCommands(len(cfg.SandboxAllowedDomains) > 0, nil)
		}
	}
	line := "on (" + src + ") " + strings.TrimPrefix(st.Describe(), "lockdown: on ")
	line += containerNote
	if domains := st.WebFetchDomains(); len(domains) > 0 {
		line += "; web_fetch after taint: " + strings.Join(domains, ", ")
	}
	if l.untrustedTask {
		line += "; run starts tainted (--untrusted-task)"
	}
	return line, nil
}
