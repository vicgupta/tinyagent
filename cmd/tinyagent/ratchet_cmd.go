package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/ratchet"
)

// ratchetFlags hold the project rules ("ratchet", issue #249) flags.
type ratchetFlags struct {
	list, path                      bool
	add, remove                     string
	incident, expires, review       string
	denyTool, denyCommand, denyPath string
	denyHost                        string
	off                             bool
}

// ratchetCommandFlags are the management commands; exactly one may be set.
var ratchetCommandFlags = []string{"ratchet-list", "ratchet-path", "ratchet-add", "ratchet-remove"}

// ratchetAddFlags only apply to --ratchet-add.
var ratchetAddFlags = []string{"ratchet-incident", "ratchet-expires", "ratchet-review", "ratchet-deny-tool", "ratchet-deny-command", "ratchet-deny-path", "ratchet-deny-host"}

// ratchetGroup lists the ratchet flags in --help --verbose.
var ratchetGroup = flagGroup{"Project rules (ratchet)", [][3]string{
	{"", "ratchet-add", "rule"}, {"", "ratchet-incident", "text"}, {"", "ratchet-expires", "date"}, {"", "ratchet-review", "text"},
	{"", "ratchet-deny-tool", "glob"}, {"", "ratchet-deny-command", "glob"}, {"", "ratchet-deny-path", "pattern"}, {"", "ratchet-deny-host", "host"},
	{"", "ratchet-list", ""}, {"", "ratchet-path", ""}, {"", "ratchet-remove", "id"}, {"", "no-ratchet", ""},
}}

func registerRatchetFlags(fs *flag.FlagSet, r *ratchetFlags) {
	fs.StringVar(&r.add, "ratchet-add", "", "record a project rule with this text in --cwd's "+ratchet.RelPath+" and exit; needs --ratchet-incident; without --ratchet-expires or --ratchet-review it expires in 90 days")
	fs.StringVar(&r.incident, "ratchet-incident", "", "with --ratchet-add: the failure that led to the rule (required)")
	fs.StringVar(&r.expires, "ratchet-expires", "", "with --ratchet-add: YYYY-MM-DD date from which the rule is skipped and flagged as expired")
	fs.StringVar(&r.review, "ratchet-review", "", "with --ratchet-add: when to reconsider the rule (e.g. \"when the default model changes\"); with it and no --ratchet-expires the rule has no expiry date")
	fs.StringVar(&r.denyTool, "ratchet-deny-tool", "", "with --ratchet-add: also enforce the rule as a permissions deny rule for these tools (\"|\"-separated globs; defaults by matcher: bash|start_process, write_file|edit_file|apply_patch, web_fetch)")
	fs.StringVar(&r.denyCommand, "ratchet-deny-command", "", "with --ratchet-add: also enforce the rule by denying shell commands matching this glob (e.g. \"git push *\")")
	fs.StringVar(&r.denyPath, "ratchet-deny-path", "", "with --ratchet-add: also enforce the rule by denying file edits to paths matching this pattern (file_access syntax)")
	fs.StringVar(&r.denyHost, "ratchet-deny-host", "", "with --ratchet-add: also enforce the rule by denying web_fetch to this host (sandbox_allowed_domains syntax)")
	fs.BoolVar(&r.list, "ratchet-list", false, "print the project rules in --cwd's "+ratchet.RelPath+" with their status (active or expired), incident and expiry or review condition (--json for a JSON array), then exit")
	fs.BoolVar(&r.path, "ratchet-path", false, "print the project rules file path for --cwd and exit")
	fs.StringVar(&r.remove, "ratchet-remove", "", "delete the project rule with this id and exit")
	fs.BoolVar(&r.off, "no-ratchet", false, "don't load the project rules in "+ratchet.RelPath+" for this run (no guidance, no enforced deny rules)")
}

// ratchetCommandSet reports whether a ratchet management flag was given.
func ratchetCommandSet(fs *flag.FlagSet) bool {
	return flagWasSet(fs, ratchetCommandFlags...) || flagWasSet(fs, ratchetAddFlags...)
}

// ratchetAllowedFlags are the only other flags a ratchet command accepts.
var ratchetAllowedFlags = map[string]bool{"cwd": true, "json": true}

// ratchetNow is the clock for created/expiry dates; tests replace it.
var ratchetNow = time.Now

// runRatchetCommand implements the ratchet management flags. Usage errors
// exit 2; a rules file that can't be read or written exits 1.
func runRatchetCommand(o *options, fs *flag.FlagSet, stdout, stderr io.Writer) int {
	var set []string
	for _, name := range ratchetCommandFlags {
		if flagWasSet(fs, name) {
			set = append(set, "--"+name)
		}
	}
	if len(set) == 0 {
		fmt.Fprintln(stderr, "error: the --ratchet-incident, --ratchet-expires, --ratchet-review and --ratchet-deny-* flags apply only to --ratchet-add")
		return 2
	}
	if len(set) > 1 {
		fmt.Fprintf(stderr, "error: %s can't be combined; run one ratchet command at a time\n", strings.Join(set, " and "))
		return 2
	}
	r := o.ratchet
	var conflict string
	fs.Visit(func(f *flag.Flag) {
		if conflict != "" || ratchetAllowedFlags[f.Name] {
			return
		}
		for _, n := range ratchetCommandFlags {
			if f.Name == n {
				return
			}
		}
		for _, n := range ratchetAddFlags {
			if f.Name == n && flagWasSet(fs, "ratchet-add") {
				return
			}
		}
		conflict = f.Name
	})
	if conflict != "" {
		dash := "--"
		if len(conflict) == 1 {
			dash = "-"
		}
		fmt.Fprintf(stderr, "error: %s only manages the project rules file; it can't be combined with %s%s\n", set[0], dash, conflict)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected positional argument %q\n", fs.Arg(0))
		return 2
	}
	if o.jsonOutput && !r.list {
		fmt.Fprintln(stderr, "error: --json applies only to --ratchet-list among the ratchet commands")
		return 2
	}
	if fi, err := os.Stat(o.cwd); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "error: --cwd %q is not a directory\n", o.cwd)
		return 2
	}
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: ratchet:", err)
		return 1
	}
	path := ratchet.Path(root)
	switch {
	case r.path:
		fmt.Fprintln(stdout, path)
		return 0
	case flagWasSet(fs, "ratchet-add"):
		rule := ratchet.Rule{Rule: strings.TrimSpace(r.add), Incident: strings.TrimSpace(r.incident), Expires: r.expires, Review: strings.TrimSpace(r.review)}
		if rule.Rule == "" {
			fmt.Fprintln(stderr, "error: --ratchet-add needs the rule text")
			return 2
		}
		if rule.Incident == "" {
			fmt.Fprintln(stderr, "error: --ratchet-add needs --ratchet-incident: what went wrong that this rule prevents")
			return 2
		}
		if r.denyTool != "" || r.denyCommand != "" || r.denyPath != "" || r.denyHost != "" {
			rule.Deny = &ratchet.Deny{Tool: r.denyTool, Command: r.denyCommand, Path: r.denyPath, Host: r.denyHost}
		}
		probe := rule
		probe.Created, probe.Expires = ratchetNow().Format(ratchet.DateLayout), "2000-01-01"
		if rule.Expires != "" {
			probe.Expires = rule.Expires
		}
		if err := probe.Validate(); err != nil {
			fmt.Fprintln(stderr, "error: --ratchet-add:", err)
			return 2
		}
		if rule.Deny != nil {
			if err := checkRatchetDeny(rule); err != nil {
				fmt.Fprintln(stderr, "error: --ratchet-add:", err)
				return 2
			}
		}
		added, err := ratchet.Add(path, rule, ratchetNow())
		if err != nil {
			fmt.Fprintln(stderr, "error: ratchet:", err)
			return 1
		}
		fmt.Fprintf(stdout, "added rule %s to %s (%s)\n", added.ID, path, ratchetLimit(added))
		return 0
	case flagWasSet(fs, "ratchet-remove"):
		id := strings.TrimSpace(r.remove)
		if id == "" {
			fmt.Fprintln(stderr, "error: --ratchet-remove needs a rule id (see --ratchet-list)")
			return 2
		}
		if err := ratchet.Remove(path, id); err != nil {
			fmt.Fprintln(stderr, "error: ratchet:", err)
			return 1
		}
		fmt.Fprintf(stdout, "removed rule %s from %s\n", id, path)
		return 0
	}
	rules, err := ratchet.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: ratchet:", err)
		return 1
	}
	now := ratchetNow()
	if o.jsonOutput {
		out := []map[string]any{}
		for _, x := range rules {
			m := map[string]any{"id": x.ID, "rule": x.Rule, "incident": x.Incident, "created": x.Created, "status": ratchetStatus(x, now), "enforced": x.Deny != nil}
			if x.Expires != "" {
				m["expires"] = x.Expires
			}
			if x.Review != "" {
				m["review"] = x.Review
			}
			if x.Deny != nil {
				m["deny"] = ratchet.PermissionRules([]ratchet.Rule{x})[0]
			}
			out = append(out, m)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return 1
		}
		return 0
	}
	if len(rules) == 0 {
		fmt.Fprintf(stdout, "no project rules (%s)\n", path)
		return 0
	}
	for _, x := range rules {
		enforced := ""
		if x.Deny != nil {
			enforced = " [enforced]"
		}
		fmt.Fprintf(stdout, "%s  %s  %s  %s%s\n    incident: %s\n    %s\n", x.ID, ratchetStatus(x, now), x.Created, x.Rule, enforced, x.Incident, ratchetLimit(x))
	}
	return 0
}

func ratchetStatus(r ratchet.Rule, now time.Time) string {
	if r.Expired(now) {
		return "expired"
	}
	return "active"
}

func ratchetLimit(r ratchet.Rule) string {
	var parts []string
	if r.Expires != "" {
		parts = append(parts, "expires "+r.Expires)
	}
	if r.Review != "" {
		parts = append(parts, "review: "+r.Review)
	}
	return strings.Join(parts, "; ")
}

// checkRatchetDeny compiles a rule's deny matcher with the permission
// policy so a bad pattern is refused when it is recorded.
func checkRatchetDeny(r ratchet.Rule) error {
	r.ID = "new"
	_, err := permissions.New(config.Permissions{Rules: ratchet.PermissionRules([]ratchet.Rule{r})})
	return err
}

// ratchetRun is the project rules a fresh run loaded.
type ratchetRun struct {
	path            string
	active, expired []ratchet.Rule
	enforced        int
}

// loadRatchet reads root's project rules unless off, and appends a deny
// rule to cfg.Permissions for each active rule with a matcher. It returns
// nil when off or when the file is missing or empty, changing nothing.
func loadRatchet(off bool, root string, cfg *config.AgentConfig) (*ratchetRun, error) {
	if off {
		return nil, nil
	}
	path := ratchet.Path(root)
	rules, err := ratchet.Load(path)
	if err != nil {
		return nil, &config.InvalidError{Err: fmt.Errorf("project rules: %w (fix the file, or run with --no-ratchet)", err)}
	}
	if len(rules) == 0 {
		return nil, nil
	}
	rr := &ratchetRun{path: path}
	rr.active, rr.expired = ratchet.Split(rules, ratchetNow())
	deny := ratchet.PermissionRules(rr.active)
	rr.enforced = len(deny)
	cfg.Permissions.Rules = append(cfg.Permissions.Rules, deny...)
	return rr, nil
}

// start puts the active rules in a's system prompt, warns about each
// expired rule, and emits ratchet.loaded.
func (rr *ratchetRun) start(a *agent.Agent, events *runEvents, warn func(string)) {
	if rr == nil {
		return
	}
	a.Ratchet = ratchet.Section(rr.active)
	ids, expired := []string{}, []string{}
	for _, r := range rr.active {
		ids = append(ids, r.ID)
	}
	for _, r := range rr.expired {
		expired = append(expired, r.ID)
		warn(fmt.Sprintf("project rule %s expired on %s and was skipped; review it, then remove it (--ratchet-remove %s) or re-add it with a new expiry", r.ID, r.Expires, r.ID))
	}
	events.emit("ratchet.loaded", map[string]any{"path": rr.path, "loaded": len(rr.active), "ids": ids, "expired": expired, "enforced": rr.enforced})
	if a.Verbose {
		fmt.Fprintf(a.Out, "project rules: loaded %d (%d enforced, %d expired) from %s\n", len(rr.active), rr.enforced, len(rr.expired), rr.path)
	}
}
