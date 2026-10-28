package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// checkACPLockdown checks the lockdown flags and config up front, before
// any session starts, the way a headless run does: --untrusted-task
// without lockdown and an invalid untrusted_content exit 2. A config that
// fails to load is left to the session, which reports it.
func checkACPLockdown(o *options, fs *flag.FlagSet) int {
	cfg, err := config.LoadProfile(config.Find(o.configPath), o.profile)
	if err != nil {
		cfg = config.AgentConfig{}
	}
	_, code := setupLockdown(fs, o.lockdown, &cfg, false, false, os.Stderr)
	return code
}

// acpSetupLockdown is setupLockdown for an ACP session: one lockdown state
// for the whole session, so taint carries over from turn to turn. With
// --untrusted-task every prompt is treated as untrusted and the session
// starts tainted.
func acpSetupLockdown(fs *flag.FlagSet, l lockdownFlags, cfg *config.AgentConfig) (*lockdown.State, error) {
	var msg bytes.Buffer
	st, code := setupLockdown(fs, l, cfg, false, false, &msg)
	if code != 0 {
		return nil, errors.New(strings.TrimPrefix(strings.TrimSpace(msg.String()), "error: "))
	}
	return st, nil
}

// startACPLockdown finishes an ACP session's lockdown setup once its
// environment and MCP servers are up: client-supplied servers taint as
// acp_client_mcp and keep the strict defaults, web_fetch checks every hop,
// and sandboxed commands keep running after taint (see
// confineLockdownCommands). It prints the startup line once per session.
// It does nothing when st is nil.
func startACPLockdown(st *lockdown.State, clientMCP []string, env tools.Environment, wf *tools.WebFetcher, stderr io.Writer) {
	if st == nil {
		return
	}
	st.SetACPClientMCPServers(clientMCP)
	if wf != nil {
		wf.Gate = st.WebFetchGate
	}
	confineLockdownEnv(st, env, stderr)
	fmt.Fprintln(stderr, st.Describe())
	for _, s := range st.Sources() {
		fmt.Fprintf(stderr, "lockdown: session starts tainted (%s)\n", s.Source)
	}
}

// emitStartTaints emits the taint events of a session that starts tainted,
// in its first turn's event log. Later taints are emitted by the turn that
// records them.
func emitStartTaints(st *lockdown.State, events *runEvents) {
	for _, s := range st.Sources() {
		events.emit("taint", s.Fields())
	}
}
