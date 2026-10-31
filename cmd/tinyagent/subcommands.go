package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Subcommands are an additional syntax over the existing flags: argv that
// starts with a subcommand is rewritten into the equivalent flags before
// flag parsing, so every --flag form keeps working unchanged and both
// spellings set the same options.
//
//	tinyagent sandbox --network -t "fix it"   == tinyagent --sandbox --sandbox-network -t "fix it"
//	tinyagent sandbox --backend bwrap -t "x"  == tinyagent --sandbox --sandbox-backend bwrap -t "x"
//	tinyagent memory list                     == tinyagent --memory-list
//	tinyagent planner --first -- fix the bug  == tinyagent --plan --plan-first -t "fix the bug"
//
// argv[1] is a subcommand only when it is exactly a subcommand name and is
// alone or followed by an option (-x), "--", or one of that subcommand's
// actions. Anything else (e.g. "tinyagent memory leaks in the parser") is
// left to the normal parser; pass a task that starts with such a word with
// -t "memory leaks ...", which never starts with a subcommand.
type subcommand struct {
	name    string
	summary string
	// brief is the one-line description in the top-level --help.
	brief string
	// base flags are added when no action is given (e.g. sandbox -> --sandbox).
	base []string
	// actions are positional words mapped to a flag; arg is true when the
	// action takes one value (memory approve <id>).
	actions map[string]subAction
	// options map a subcommand option name to the global flag it sets.
	options map[string]string
	// runBare runs the base flags even for a bare "tinyagent <sub>" at a
	// terminal instead of printing help: for a complete one-shot command
	// (init) that needs no task, help would only add a step.
	runBare bool
	// positional names the flag a leading non-option word sets, and
	// positionalArg its placeholder in help (triage <issues.json>).
	positional, positionalArg string
}

type subAction struct {
	flag string
	arg  bool
	// arg2 means the action takes two values, joined as "<a>@<b>".
	arg2 bool
	// args means the action takes one or more values up to the next
	// option, each passed as its own --flag value.
	args bool
	desc string
	// withBase keeps the base flags (sandbox network still runs sandboxed).
	withBase bool
}

var subcommands = []subcommand{
	{
		name: "chat", brief: "start an interactive session (REPL)", summary: "start an interactive chat session on the terminal: each message is one turn of the same conversation (same as --interactive / -i); -- <msg> or -t sends the first message",
		base:    []string{"--interactive"},
		runBare: true,
	},
	{
		name: "init", brief: "write ./tinyagent.yaml in --cwd (--force, --migrate)", summary: "write a commented starter ./tinyagent.yaml in --cwd, or with -c to that path (same as --init-config)",
		base:    []string{"--init-config"},
		runBare: true,
		options: map[string]string{"force": "init-force", "migrate": "init-migrate"},
	},
	{
		name: "sandbox", brief: "run a task with commands confined to --cwd", summary: "run with commands confined to --cwd (same as --sandbox)",
		base:    []string{"--sandbox"},
		actions: map[string]subAction{"network": {flag: "sandbox-network", withBase: true, desc: "also allow commands network access (--sandbox-network)"}},
		options: map[string]string{"network": "sandbox-network", "allow-domain": "sandbox-allow-domain", "allowed-domains": "sandbox-allow-domain", "backend": "sandbox-backend"},
	},
	{
		name: "memory", brief: "run with project memory, or list|path|approve|forget|config", summary: "run with project memory, or manage the memory store (same as --memory*)",
		base: []string{"--memory"},
		actions: map[string]subAction{
			"list":    {flag: "memory-list", desc: "print the memory entries and exit (--memory-list)"},
			"path":    {flag: "memory-path", desc: "print the memory store path and exit (--memory-path)"},
			"approve": {flag: "memory-approve", arg: true, desc: "approve a pending entry by id, or all (--memory-approve)"},
			"forget":  {flag: "memory-forget", arg: true, desc: "delete an entry by id, or all with --yes (--memory-forget)"},
			"config":  {flag: "memory-help", desc: "print every memory.toml key and exit (--memory-help)"},
		},
		options: map[string]string{"list": "memory-list", "path": "memory-path", "approve": "memory-approve", "forget": "memory-forget", "yes": "memory-yes", "config-help": "memory-help"},
	},
	{
		name: "ratchet", brief: "record project rules from agent failures: add|list|remove|path", summary: "record and manage project rules learned from agent failures, each with its incident and expiry (same as --ratchet-*)",
		actions: map[string]subAction{
			"add":    {flag: "ratchet-add", arg: true, desc: "record a rule with this text; needs --incident (--ratchet-add)"},
			"list":   {flag: "ratchet-list", desc: "print every rule with its status, incident and expiry, then exit (--ratchet-list)"},
			"remove": {flag: "ratchet-remove", arg: true, desc: "delete a rule by id (--ratchet-remove)"},
			"path":   {flag: "ratchet-path", desc: "print the rules file path and exit (--ratchet-path)"},
		},
		options: map[string]string{"incident": "ratchet-incident", "expires": "ratchet-expires", "review": "ratchet-review", "deny-tool": "ratchet-deny-tool", "deny-command": "ratchet-deny-command", "deny-path": "ratchet-deny-path", "deny-host": "ratchet-deny-host"},
	},
	{
		name: "planner", brief: "run a task with the update_plan planner (--first, --file)", summary: "run with the task planner (update_plan) on (same as --plan)",
		base:    []string{"--plan"},
		options: map[string]string{"first": "plan-first", "file": "plan-file"},
	},
	{
		name: "plugins", brief: "manage plugins: list|prune|rollback", summary: "inspect and manage plugins and stored plugin modules (same as --list-plugins and --plugins-*)",
		actions: map[string]subAction{
			"list":     {flag: "list-plugins", desc: "print each plugin, its skills and servers, then exit (--list-plugins)"},
			"prune":    {flag: "plugins-prune", desc: "keep only current + the newest --keep stored module versions (--plugins-prune)"},
			"rollback": {flag: "plugins-rollback", arg2: true, desc: "point <module> at stored version <sha256> (--plugins-rollback <module>@<sha256>)"},
		},
		options: map[string]string{"keep": "plugins-keep"},
	},
	{
		name: "skills", brief: "check workflow skill files and workflow files: validate", summary: "check skill-workflow Markdown skills and YAML workflow files without running anything (same as --skills-validate); not Agent Skills (--skills)",
		actions: map[string]subAction{
			"validate": {flag: "skills-validate", args: true, desc: "validate skill files, skills directories or workflow files, then exit (--skills-validate <path>, repeatable)"},
		},
	},
	{
		name: "triage", brief: "rank open issues by value/effort into a next-up list", summary: "rank the open issues in a gh issue list JSON file by value and effort, offline, flag likely duplicates, stale and blocked items, and print a next-up list (same as --triage)",
		positional: "triage", positionalArg: "issues.json",
		options: map[string]string{"out": "triage-out", "stale-days": "triage-stale-days"},
	},
	{
		name: "tinygo", brief: "manage the external TinyGo compiler: install|update|status", summary: "manage the TinyGo compiler for Go plugin modules (a separate, downloaded project; same as --tinygo-*)",
		actions: map[string]subAction{
			"install": {flag: "tinygo-install", desc: "download, verify and cache the pinned TinyGo release (--tinygo-install)"},
			"update":  {flag: "tinygo-update", desc: "move to the TinyGo version pinned in this build, removing old ones (--tinygo-update)"},
			"status":  {flag: "tinygo-status", desc: "print pinned and installed versions, path and checksum state (--tinygo-status)"},
		},
	},
}

func findSubcommand(name string) *subcommand {
	for i := range subcommands {
		if subcommands[i].name == name {
			return &subcommands[i]
		}
	}
	return nil
}

// subHelp is set when argv asked for a subcommand's help; run prints it.
type subHelp struct{ sub *subcommand }

// expandSubcommand rewrites args (without the program name). It returns args
// unchanged when they don't start with a subcommand, help for
// "tinyagent <sub> --help" / "tinyagent help <sub>", or an error for an
// unknown subcommand option.
func expandSubcommand(args []string) ([]string, *subHelp, error) {
	if len(args) == 0 {
		return args, nil, nil
	}
	if args[0] == "help" && len(args) == 2 {
		if s := findSubcommand(args[1]); s != nil {
			return nil, &subHelp{s}, nil
		}
		return nil, nil, fmt.Errorf("unknown subcommand %q; subcommands: %s", args[1], subcommandNames())
	}
	s := findSubcommand(args[0])
	if s == nil {
		return args, nil, nil
	}
	rest := args[1:]
	// A bare subcommand typed at a terminal prints its help, like
	// "tinyagent <sub> --help" and like plain "tinyagent" (#242). With stdin
	// piped or redirected, one with base flags keeps running them (a
	// missing -t still fails as before), so scripts see no change.
	if len(rest) == 0 && !s.runBare && (len(s.base) == 0 || stdinIsTerminal()) {
		return nil, &subHelp{s}, nil
	}
	if len(rest) > 0 {
		first := rest[0]
		_, isAction := s.actions[first]
		if !strings.HasPrefix(first, "-") && !isAction && first != "help" && s.positional == "" {
			return args, nil, nil // a prompt-like word: not a subcommand
		}
	}
	gfs := newFlagSet(&options{})
	var out []string
	action := false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "help" && i == 0 {
			return nil, &subHelp{s}, nil
		}
		if act, ok := s.actions[a]; ok && i == 0 {
			action = !act.withBase
			if act.args {
				j := i + 1
				for ; j < len(rest) && !strings.HasPrefix(rest[j], "-"); j++ {
					out = append(out, "--"+act.flag, rest[j])
				}
				if j == i+1 {
					return nil, nil, fmt.Errorf("%s %s needs at least one value", s.name, a)
				}
				i = j - 1
			} else if act.arg2 {
				if i+2 >= len(rest) {
					return nil, nil, fmt.Errorf("%s %s needs two values", s.name, a)
				}
				out = append(out, "--"+act.flag, rest[i+1]+"@"+rest[i+2])
				i += 2
			} else if act.arg {
				if i+1 >= len(rest) {
					return nil, nil, fmt.Errorf("%s %s needs a value", s.name, a)
				}
				out = append(out, "--"+act.flag, rest[i+1])
				i++
			} else {
				out = append(out, "--"+act.flag)
			}
			continue
		}
		if s.positional != "" && i == 0 && (!strings.HasPrefix(a, "-") || a == "-") {
			out = append(out, "--"+s.positional, a)
			continue
		}
		if a == "--" {
			task := strings.TrimSpace(strings.Join(rest[i+1:], " "))
			if task == "" {
				return nil, nil, fmt.Errorf("%s: nothing after --; pass the task after it", s.name)
			}
			out = append(out, "-t", task)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return nil, nil, fmt.Errorf("unexpected argument %q after %q; pass the task with -t or after --", a, s.name)
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "h" || name == "help" {
			return nil, &subHelp{s}, nil
		}
		target, isOpt := s.options[name]
		if !isOpt {
			if gfs.Lookup(name) == nil {
				return nil, nil, fmt.Errorf("unknown %s option %s (see tinyagent %s --help)", s.name, a, s.name)
			}
			target = name
		}
		tok := "--" + target
		if hasVal {
			tok += "=" + val
		}
		out = append(out, tok)
		if !hasVal && !isBoolFlag(gfs.Lookup(target)) {
			if i+1 >= len(rest) {
				return nil, nil, fmt.Errorf("%s option %s needs a value", s.name, a)
			}
			out = append(out, rest[i+1])
			i++
		}
	}
	if !action {
		out = append(append([]string(nil), s.base...), out...)
		if len(s.base) == 0 && len(out) == 0 {
			return nil, &subHelp{s}, nil
		}
	}
	return out, nil, nil
}

func isBoolFlag(f *flag.Flag) bool {
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func subcommandNames() string {
	var n []string
	for _, s := range subcommands {
		n = append(n, s.name)
	}
	return strings.Join(n, ", ")
}

// printSubcommandHelp prints one subcommand's grouped help.
func printSubcommandHelp(w io.Writer, s *subcommand) {
	fs := newFlagSet(&options{})
	fmt.Fprintf(w, "Usage: tinyagent %s", s.name)
	if s.positionalArg != "" {
		fmt.Fprintf(w, " <%s>", s.positionalArg)
	}
	if len(s.actions) > 0 {
		fmt.Fprint(w, " [action]")
	}
	if len(s.options) > 0 {
		fmt.Fprint(w, " [options]")
	}
	if len(s.base) > 0 && !s.runBare {
		fmt.Fprint(w, " [-t \"<prompt>\" | -- <prompt>]")
	}
	fmt.Fprintf(w, "\n%s.\n", strings.ToUpper(s.summary[:1])+s.summary[1:])
	if s.runBare {
		fmt.Fprintf(w, "Run bare, it does this right away (%s).\n", strings.Join(s.base, " "))
	} else if len(s.base) > 0 {
		fmt.Fprintf(w, "Without an action it runs a task with %s.\n", strings.Join(s.base, " "))
	}
	if len(s.actions) > 0 {
		fmt.Fprintln(w, "\nActions:")
		var rows [][2]string
		for _, k := range subKeys(s.actions) {
			a := s.actions[k]
			arg := ""
			if a.arg {
				arg = " <value>"
			}
			if a.arg2 {
				arg = " <module> <sha256>"
			}
			if a.args {
				arg = " <path>..."
			}
			rows = append(rows, [2]string{k + arg, a.desc})
		}
		printRows(w, rows)
	}
	if len(s.options) > 0 {
		fmt.Fprintln(w, "\nOptions:")
		var rows [][2]string
		for _, k := range subKeys(s.options) {
			f := fs.Lookup(s.options[k])
			arg := ""
			if !isBoolFlag(f) {
				arg = " <value>"
			}
			rows = append(rows, [2]string{"--" + k + arg, "same as --" + s.options[k] + ": " + f.Usage})
		}
		printRows(w, rows)
	}
	if s.runBare {
		fmt.Fprintln(w, "\n--cwd <dir> and -c <path> choose where the file goes.\nEvery --flag form keeps working without the subcommand.")
	} else if len(s.base) > 0 {
		fmt.Fprintf(w, "\nAny other tinyagent flag (-t, -m, --cwd, ...) may follow.\nEvery --flag form keeps working without the subcommand.\nA task starting with %q is passed with -t, e.g. tinyagent -t %q.\n", s.name, s.name+" ...")
	} else {
		fmt.Fprintln(w, "\nEvery --flag form keeps working without the subcommand.")
	}
	fmt.Fprintln(w, "Full reference: docs/subcommands.md.")
}

func subKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// printRows prints name/description rows with the descriptions in one
// column, wrapped at helpWidth like --help --verbose.
func printRows(w io.Writer, rows [][2]string) {
	width := 0
	for _, r := range rows {
		width = max(width, len(r[0]))
	}
	col := width + 5
	for _, r := range rows {
		lines := wrapWords(r[1], max(helpWidth-col, 30))
		fmt.Fprintf(w, "  %-*s%s\n", col-2, r[0], lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", col+2), l)
		}
	}
}
