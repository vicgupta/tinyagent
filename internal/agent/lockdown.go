package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// lockdownChannel names the outbound channel a call to name uses, or ""
// for tools lockdown leaves alone (file tools, plans, processes already
// started, submission, ...).
func (a *Agent) lockdownChannel(name string) string {
	switch {
	case name == tools.WebFetchToolName && a.Tools.WebFetch != nil:
		return lockdown.ChannelWebFetch
	case name == "bash" || name == "start_process":
		return lockdown.ChannelCommand
	}
	for _, c := range a.Tools.CommandTools {
		if c.Name == name {
			return lockdown.ChannelCommand
		}
	}
	for _, s := range a.Tools.ExternalSpecs {
		if s.Function.Name == name {
			return lockdown.ChannelMCP
		}
	}
	return ""
}

// webFetchURLHost returns the first host a web_fetch call would connect to.
func webFetchURLHost(raw string) (host string, port int, ok bool) {
	var args struct {
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(raw), &args) != nil {
		return "", 0, false
	}
	return lockdown.URLHostPort(args.URL)
}

// lockdownRefuse refuses a call on a restricted channel once the run is
// tainted. It runs before the permission policy and pre_tool hooks, so
// neither can allow what lockdown refuses, and a refused call reaches no
// hook, server or network.
func (a *Agent) lockdownRefuse(t *trajectory.Trajectory, call model.ToolCall) (tools.Result, bool) {
	if a.Lockdown == nil || !a.Lockdown.Tainted() || !a.Tools.Permits(call.Function.Name) {
		return tools.Result{}, false
	}
	name := call.Function.Name
	channel := a.lockdownChannel(name)
	if channel == "" {
		return tools.Result{}, false
	}
	host := ""
	if channel == lockdown.ChannelWebFetch {
		h, port, ok := webFetchURLHost(call.Function.Arguments)
		if ok && a.Lockdown.WebFetchAllowed(h, port) {
			return tools.Result{}, false
		}
		host = h
	}
	if channel == lockdown.ChannelMCP && a.Lockdown.MCPCallAllowed(a.mcpServersOf(name, call.Function.Arguments)) {
		return tools.Result{}, false
	}
	first, _ := a.Lockdown.First()
	d := lockdown.Denial{Channel: channel, Tool: name, ToolCallID: call.ID, Step: t.Steps, Host: host}
	a.Lockdown.Deny(d)
	a.event("lockdown.denied", d.Fields())
	if a.Verbose {
		what := name
		if host != "" {
			what += " " + host
		}
		fmt.Fprintf(a.Err, "lockdown: refused %s (%s, run tainted by %s at step %d)\n", what, channel, first.Source, first.Step)
	}
	msg := lockdown.RefusalMessage(name, channel, first, host)
	return tools.Result{Text: tools.Truncate(msg, a.Tools.MaxOutputChars), IsError: true}, true
}

// lockdownTaint records the taint a finished call brings: any web_fetch or
// MCP result, successful or not, since an error can carry the server's
// text too. The first taint of the run appends lockdown.Notice to the
// result and stops the background processes still running, whose network
// lockdown can't restrict.
func (a *Agent) lockdownTaint(t *trajectory.Trajectory, call model.ToolCall, res *tools.Result) {
	if a.Lockdown == nil {
		return
	}
	src := lockdown.Source{Step: t.Steps, ToolCallID: call.ID, Tool: call.Function.Name}
	switch a.lockdownChannel(call.Function.Name) {
	case lockdown.ChannelWebFetch:
		src.Source = lockdown.SourceWebFetch
		src.Host, _, _ = webFetchURLHost(call.Function.Arguments)
	case lockdown.ChannelMCP:
		if !a.Lockdown.MCPCallTaints(a.mcpServersOf(call.Function.Name, call.Function.Arguments)) {
			return
		}
		src.Source = lockdown.SourceMCP
	default:
		return
	}
	added, first := a.Lockdown.Taint(src)
	if !added {
		return
	}
	a.event("taint", src.Fields())
	if a.Verbose {
		fmt.Fprintf(a.Err, "lockdown: run tainted by %s (%s, step %d)\n", src.Source, src.Tool, src.Step)
	}
	if !first {
		return
	}
	notice := lockdown.Notice
	if stopped := a.stopProcessesForLockdown(); len(stopped) > 0 {
		notice += fmt.Sprintf("\n[tinyagent: stopped background processes %s: their network can't be restricted]", strings.Join(stopped, ", "))
	}
	res.Text += "\n\n" + notice
}

// stopProcessesForLockdown stops every running background process and
// returns their ids.
func (a *Agent) stopProcessesForLockdown() []string {
	var stopped []string
	for _, p := range a.Tools.Processes() {
		if !p.Running {
			continue
		}
		if _, err := a.Tools.StopProcess(p.ID, 0); err == nil {
			stopped = append(stopped, p.ID)
		}
	}
	if len(stopped) > 0 {
		a.event("lockdown.processes_stopped", map[string]any{"ids": stopped})
	}
	return stopped
}

// lockdownExec runs call and records the taint its result brings.
func (a *Agent) lockdownExec(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) tools.Result {
	res := a.execTool(ctx, t, call)
	a.lockdownTaint(t, call, &res)
	return res
}
