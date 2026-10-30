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
	// base flags are added when no action is given (e.g. sandbox -> --sandbox).
	base []string
	// actions are positional words mapped to a flag; arg is true when the
	// action takes one value (memory approve <id>).
	actions map[string]subAction
	// options map a subcommand option name to the global flag it sets.
	options map[string]string
}

type subAction struct {
	flag string
	arg  bool
	desc string
	// withBase keeps the base flags (sandbox network still runs sandboxed).
	withBase bool
}

var subcommands = []subcommand{
	{
		name: "sandbox", summary: "run with commands confined to --cwd (same as --sandbox)",
		base:    []string{"--sandbox"},
		actions: map[string]subAction{"network": {flag: "sandbox-network", withBase: true, desc: "also allow commands network access (--sandbox-network)"}},
		options: map[string]string{"network": "sandbox-network", "allow-domain": "sandbox-allow-domain", "allowed-domains": "sandbox-allow-domain"},
	},
	{
		name: "memory", summary: "run with project memory, or manage the memory store (same as --memory*)",
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
		name: "planner", summary: "run with the task planner (update_plan) on (same as --plan)",
		base:    []string{"--plan"},
		options: map[string]string{"first": "plan-first", "file": "plan-file"},
	},
	{
		name: "plugins", summary: "inspect configured plugins (same as --list-plugins)",
		actions: map[string]subAction{"list": {flag: "list-plugins", desc: "print each plugin, its skills and servers, then exit (--list-plugins)"}},
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
	if len(rest) > 0 {
		first := rest[0]
		_, isAction := s.actions[first]
		if !strings.HasPrefix(first, "-") && !isAction && first != "help" {
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
			if act.arg {
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
	if len(s.actions) > 0 {
		fmt.Fprint(w, " [action]")
	}
	fmt.Fprintf(w, " [options] [-t \"<prompt>\" | -- <prompt>]\n%s.\n", strings.ToUpper(s.summary[:1])+s.summary[1:])
	if len(s.base) > 0 {
		fmt.Fprintf(w, "Without an action it runs a task with %s.\n", strings.Join(s.base, " "))
	}
	if len(s.actions) > 0 {
		fmt.Fprintln(w, "\nActions:")
		for _, k := range subKeys(s.actions) {
			a := s.actions[k]
			arg := ""
			if a.arg {
				arg = " <value>"
			}
			fmt.Fprintf(w, "  %-22s %s\n", k+arg, a.desc)
		}
	}
	if len(s.options) > 0 {
		fmt.Fprintln(w, "\nOptions:")
		for _, k := range subKeys(s.options) {
			f := fs.Lookup(s.options[k])
			arg := ""
			if !isBoolFlag(f) {
				arg = " <value>"
			}
			fmt.Fprintf(w, "  --%-20s same as --%s: %s\n", k+arg, s.options[k], f.Usage)
		}
	}
	fmt.Fprintf(w, "\nAny other tinyagent flag (-t, -m, --cwd, ...) may follow. Every --flag form keeps working without the subcommand.\nA task starting with %q is passed with -t, e.g. tinyagent -t %q.\n", s.name, s.name+" ...")
}

func subKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
