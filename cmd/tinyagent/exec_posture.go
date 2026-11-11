package main

import (
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
)

// Execution posture (issue #415): how the built-in commands run and which
// network web tools are on. A run prints the unsandboxed warning on stderr
// every time (there is no setting that hides it), and --check reports the
// same state in its text and JSON output.

// Command modes reported by --check's commands.mode.
const (
	commandsUnsandboxed = "unsandboxed"
	commandsSandboxed   = "sandboxed"
	commandsContainer   = "container"
	commandsNone        = "none"
)

// checkCommandsInfo is the --check JSON "commands" object.
type checkCommandsInfo struct {
	Mode        string `json:"mode"`        // unsandboxed, sandboxed, container or none (read-only)
	Environment string `json:"environment"` // full, filtered (command_env) or none
	Warning     string `json:"warning"`     // the run's stderr warning, "" when there is none
}

// checkWebToolsInfo is the --check JSON "web_tools" object.
type checkWebToolsInfo struct {
	WebFetch        bool     `json:"web_fetch"`
	WebFetchDomains []string `json:"web_fetch_domains"` // empty: any public host
	WebSearch       bool     `json:"web_search"`
	ReadOnly        bool     `json:"read_only"` // on even though --read-only is set
}

// commandsPosture describes how bash, start_process and command tools run
// under cfg (after the read-only, sandbox and environment flags are
// applied).
func commandsPosture(cfg config.AgentConfig) checkCommandsInfo {
	env := "full"
	if cfg.CommandEnv.Configured() {
		env = "filtered"
	}
	switch {
	case cfg.ReadOnly:
		return checkCommandsInfo{Mode: commandsNone, Environment: "none"}
	case cfg.Environment == "container":
		return checkCommandsInfo{Mode: commandsContainer, Environment: "none"}
	case cfg.Sandbox:
		return checkCommandsInfo{Mode: commandsSandboxed, Environment: env}
	}
	return checkCommandsInfo{Mode: commandsUnsandboxed, Environment: env, Warning: unsandboxedWarning(cfg)}
}

// unsandboxedWarning is the text of the warning an unsandboxed local run
// prints (without the "warning: " prefix).
func unsandboxedWarning(cfg config.AgentConfig) string {
	env := "your full environment (API keys and other secrets included)"
	if cfg.CommandEnv.Configured() {
		env = "the environment command_env allows"
	}
	fix := "--sandbox confines them; --read-only runs none"
	if runtime.GOOS == "windows" {
		fix = "--env container isolates them; --read-only runs none"
	}
	return "commands run unsandboxed, as your user, with " + env + " and can read anything you can; " + fix
}

// warnExecPosture prints the run's posture warnings on w (stderr): the
// unsandboxed warning for a local run without --sandbox, and, for a
// read-only run, a note that the web tools still make network requests.
// cfg must already have its web tools resolved (newWebFetcher and
// newWebSearcher). Nothing can turn these lines off; they never go to
// stdout, so --json output stays valid.
func warnExecPosture(w io.Writer, cfg config.AgentConfig) {
	if p := commandsPosture(cfg); p.Warning != "" {
		fmt.Fprintln(w, "warning:", p.Warning)
	}
	if !cfg.ReadOnly {
		return
	}
	if on := webToolsOn(cfg.WebFetch.On(false), cfg.WebSearch.On(false)); len(on) > 0 {
		verb, fix := "still make", "turn them off"
		if len(on) == 1 {
			verb, fix = "still makes", "turns it off"
		}
		fmt.Fprintf(w, "read-only mode: %s %s network requests; %s %s\n", strings.Join(on, " and "), verb, webToolsOff(on), fix)
	}
}

// webToolsOn lists the web tools that are on.
func webToolsOn(fetch, search bool) []string {
	var on []string
	if fetch {
		on = append(on, "web_fetch")
	}
	if search {
		on = append(on, "web_search")
	}
	return on
}

// webToolsOff names the flags that turn the listed web tools off.
func webToolsOff(on []string) string {
	var flags []string
	for _, n := range on {
		flags = append(flags, "--no-"+strings.ReplaceAll(n, "_", "-"))
	}
	return strings.Join(flags, " and ")
}

// checkPosture resolves the commands and web tools state a run would use,
// without printing anything, for --check.
func checkPosture(fs *flag.FlagSet, o options, cfg config.AgentConfig) (checkCommandsInfo, checkWebToolsInfo, error) {
	if flagWasSet(fs, "read-only") {
		cfg.ReadOnly = o.readOnly
	}
	applySandboxFlags(fs, &cfg, o)
	lock := lockdownRequested(fs, o, &cfg)
	fetch, err := webFetchFlag(fs, o)
	if err != nil {
		return checkCommandsInfo{}, checkWebToolsInfo{}, &config.InvalidError{Err: err}
	}
	if fetch != nil {
		cfg.WebFetch.Enabled = fetch
	}
	search, err := webSearchFlag(fs, o)
	if err != nil {
		return checkCommandsInfo{}, checkWebToolsInfo{}, &config.InvalidError{Err: err}
	}
	if search != nil {
		cfg.WebSearch.Enabled = search
	}
	web := checkWebToolsInfo{WebFetch: cfg.WebFetch.On(lock), WebSearch: cfg.WebSearch.On(lock), WebFetchDomains: []string{}}
	if web.WebFetch {
		web.WebFetchDomains = append(web.WebFetchDomains, cfg.WebFetch.AllowedDomains...)
		if flagWasSet(fs, "web-fetch-allow-domain") {
			web.WebFetchDomains = append([]string{}, o.webFetchAllowDomains...)
		}
	}
	web.ReadOnly = cfg.ReadOnly && (web.WebFetch || web.WebSearch)
	return commandsPosture(cfg), web, nil
}

// commandsLine is the --check commands line.
func commandsLine(p checkCommandsInfo) string {
	switch p.Mode {
	case commandsNone:
		return "none (--read-only)"
	case commandsContainer:
		return "in the container environment"
	case commandsSandboxed:
		return "sandboxed, " + p.Environment + " environment (see sandbox)"
	}
	return "UNSANDBOXED, as your user, " + p.Environment + " environment (pass --sandbox to confine them)"
}

// webLine is the --check web line.
func webLine(w checkWebToolsInfo) string {
	fetch := "web_fetch off"
	if w.WebFetch {
		fetch = "web_fetch on (any public host)"
		if n := len(w.WebFetchDomains); n > 0 {
			fetch = fmt.Sprintf("web_fetch on (%d allowed: %s)", n, strings.Join(w.WebFetchDomains, ", "))
		}
	}
	search := "web_search off"
	if w.WebSearch {
		search = "web_search on"
	}
	line := fetch + ", " + search
	if on := webToolsOn(w.WebFetch, w.WebSearch); len(on) > 0 {
		pron, verb := "them", "turn"
		if len(on) == 1 {
			pron, verb = "it", "turns"
		}
		if w.ReadOnly {
			line += "; --read-only does not turn " + pron + " off"
		}
		line += "; " + webToolsOff(on) + " " + verb + " " + pron + " off"
	}
	return line
}

// webToolsSummary is the --check summary table's web tools cell.
func webToolsSummary(w checkWebToolsInfo) string {
	if on := webToolsOn(w.WebFetch, w.WebSearch); len(on) > 0 {
		return strings.Join(on, ", ")
	}
	return "off"
}
