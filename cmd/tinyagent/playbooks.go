package main

// Playbook routing (docs/playbooks.md). The router lives in
// internal/playbooks; this file holds its flags, the playbooks subcommand
// and the small hooks run, runResume, chat and --check call.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/cwdpath"
	"github.com/vicgupta/tinyagent/internal/harness"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/playbooks"
	"github.com/vicgupta/tinyagent/internal/routing"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// playbookOpts are the playbook flags.
type playbookOpts struct {
	on, noFallback, list, force bool
	name, validate, install     string
	// state is the run's routing state, set by playbookGate.
	state *playbookState
}

type playbookState struct {
	root     string
	pbs      []*playbooks.Playbook
	fallback bool
	sel      playbooks.Selection
	decided  bool // sel is final (named, or chosen at the p/w question)
	info     *trajectory.PlaybookInfo
}

func addPlaybookFlags(fs *flag.FlagSet, o *playbookOpts) {
	fs.BoolVar(&o.on, "playbooks", false, "route the task to a playbook in ./playbooks inside --cwd (default off; config playbooks)")
	fs.StringVar(&o.name, "playbook", "", "follow this playbook from ./playbooks (implies --playbooks, skips routing)")
	fs.BoolVar(&o.noFallback, "no-playbook-fallback", false, "with --playbooks: don't ask the auxiliary model when keywords tie or miss (config playbook_fallback: false)")
	fs.BoolVar(&o.list, "playbooks-list", false, "list the playbooks in ./playbooks and exit (tinyagent playbooks list)")
	fs.StringVar(&o.validate, "playbooks-validate", "", "check one playbook file and exit (tinyagent playbooks validate <path>)")
	fs.StringVar(&o.install, "playbooks-install", "", "write the sample playbooks to ./playbooks and exit; the value is samples (tinyagent playbooks install samples)")
	fs.BoolVar(&o.force, "playbooks-force", false, "with --playbooks-install: overwrite existing files")
}

var playbooksSubcommand = subcommand{
	name: "playbooks", brief: "list, validate or install playbooks: list|validate|install", summary: "list, check or install playbooks in ./playbooks; makes no model calls (same as --playbooks-*)",
	actions: map[string]subAction{
		"list":     {flag: "playbooks-list", desc: "print the playbooks found in ./playbooks and exit (--playbooks-list)"},
		"validate": {flag: "playbooks-validate", arg: true, desc: "check one playbook file and exit 0 (valid) or 1 (--playbooks-validate)"},
		"install":  {flag: "playbooks-install", arg: true, desc: "install samples: write the six sample playbooks to ./playbooks; --force overwrites (--playbooks-install)"},
	},
	options: map[string]string{"force": "playbooks-force"},
}

func playbooksCommandSet(o *options) bool {
	return o.pb.list || o.pb.validate != "" || o.pb.install != ""
}

// runPlaybooksCommand runs playbooks list|validate|install. No model calls.
func runPlaybooksCommand(o *options, stdout, stderr io.Writer) int {
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		fmt.Fprintf(stderr, "error: --cwd %s: %v\n", o.cwd, err)
		return 2
	}
	switch {
	case o.pb.install != "":
		if o.pb.install != "samples" {
			fmt.Fprintf(stderr, "error: unknown install target %q; use: tinyagent playbooks install samples\n", o.pb.install)
			return 2
		}
		written, skipped, err := playbooks.Install(root, o.pb.force)
		for _, p := range written {
			fmt.Fprintln(stdout, "wrote "+relTo(root, p))
		}
		for _, p := range skipped {
			fmt.Fprintln(stdout, "kept "+relTo(root, p)+" (exists; --force overwrites)")
		}
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		return 0
	case o.pb.validate != "":
		return validatePlaybook(root, o.pb.validate, stdout, stderr)
	}
	pbs, warns := playbooks.Discover(root)
	for _, w := range warns {
		fmt.Fprintln(stderr, "warning: "+w)
	}
	if len(pbs) == 0 {
		fmt.Fprintln(stdout, "no playbooks in ./playbooks (tinyagent playbooks install samples writes six)")
		return 0
	}
	for _, p := range pbs {
		flags := p.Format
		if p.ReadOnly {
			flags += ", read-only"
		}
		if p.Workflow != "" {
			flags += ", workflow " + p.Workflow
		}
		intent := p.Intent
		if len(intent) > 80 {
			intent = intent[:77] + "..."
		}
		fmt.Fprintf(stdout, "%-14s %-40s %s (%s)\n", p.Name, relTo(root, p.Path), intent, flags)
	}
	return 0
}

func validatePlaybook(root, ref string, stdout, stderr io.Writer) int {
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	resolved, err := cwdpath.Resolve(root, path)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	if fi, err := os.Stat(resolved); err == nil && fi.IsDir() {
		resolved, path = filepath.Join(resolved, "PLAYBOOK.md"), filepath.Join(path, "PLAYBOOK.md")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	if filepath.Base(path) == "PLAYBOOK.md" {
		name = filepath.Base(filepath.Dir(path))
	}
	p, err := playbooks.Parse(path, name, data)
	if err != nil {
		fmt.Fprintf(stdout, "%s: invalid: %v\n", ref, err)
		return 1
	}
	p.SkillsDir = filepath.Join(root, "skills")
	_, warns := p.LoadSkills(root)
	for _, w := range warns {
		fmt.Fprintln(stderr, "warning: "+w)
	}
	fmt.Fprintf(stdout, "%s: ok: %s, %d steps, %d done command(s)\n", ref, p.Name, len(p.Steps), len(p.Checks))
	return 0
}

// playbooksOn reports whether routing is on: --playbook, --playbooks or
// config playbooks.
func playbooksOn(fs *flag.FlagSet, o *options, cfg config.AgentConfig) bool {
	if o.pb.name != "" {
		return true
	}
	if flagWasSet(fs, "playbooks") {
		return o.pb.on
	}
	return cfg.Playbooks
}

func playbookFallback(fs *flag.FlagSet, o *options, cfg config.AgentConfig) bool {
	if flagWasSet(fs, "no-playbook-fallback") {
		return !o.pb.noFallback
	}
	return cfg.PlaybookFallback == nil || *cfg.PlaybookFallback
}

// playbookAsk asks the p/w question; tests replace it.
var playbookAsk = func(stdin io.Reader, stderr io.Writer, pb, wf string) (string, error) {
	r := bufio.NewReader(stdin)
	for range 3 {
		fmt.Fprintf(stderr, "Use playbook %q or workflow %q? [p/w] ", pb, wf)
		line, err := r.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "p", "playbook":
			return "p", nil
		case "w", "workflow":
			return "w", nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("no answer")
}

// playbookInteractive reports whether the p/w question may be asked.
var playbookInteractive = func(o *options) bool {
	return !o.jsonOutput && o.task != "-" && stdinIsTerminal() && isTerminal(os.Stderr)
}

// playbookGate runs before the workflow dispatch: it discovers playbooks
// when routing is on and settles a --playbook choice and the question of
// a playbook and a --workflow both matching, all without a model call.
// skipWorkflow means the playbook won and --workflow is dropped.
func playbookGate(o *options, fs *flag.FlagSet, stdin io.Reader, stderr io.Writer) (skipWorkflow bool, code int, done bool) {
	if o.check || o.interactive || o.resume != "" {
		return false, 0, false
	}
	_, cfg, err := loadConfig(o, o.cwd, io.Discard)
	if err != nil || !playbooksOn(fs, o, cfg) {
		return false, 0, false // config errors are reported by the run
	}
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return false, 0, false
	}
	pbs, warns := playbooks.Discover(root)
	for _, w := range warns {
		fmt.Fprintln(stderr, "warning: "+w)
	}
	st := &playbookState{root: root, pbs: pbs, fallback: playbookFallback(fs, o, cfg)}
	wf := flagWasSet(fs, "workflow")
	if o.pb.name != "" {
		if wf && o.workflowName != "" {
			fmt.Fprintln(stderr, "error: --playbook and --workflow-name both choose; pass only one")
			return false, 2, true
		}
		p, ok := playbooks.Get(pbs, o.pb.name)
		if !ok {
			fmt.Fprintf(stderr, "error: no playbook %q in ./playbooks (have: %s)\n", o.pb.name, strings.Join(playbooks.Names(pbs), ", "))
			return false, 2, true
		}
		st.sel, st.decided = playbooks.Selection{Playbook: p, Method: playbooks.MethodNamed}, true
		o.pb.state = st
		if wf {
			o.workflow = ""
			return true, 0, false
		}
		return false, 0, false
	}
	if !wf {
		o.pb.state = st
		return false, 0, false
	}
	// --workflow given: a named workflow wins; otherwise ask when both match.
	if o.workflowName != "" || o.task == "" {
		return false, 0, false
	}
	task, err := readTask(o.task, stdin)
	if err != nil {
		return false, 0, false
	}
	interactive := playbookInteractive(o)
	o.task = task // read once; later readers get the text
	p, words := playbooks.Keywords(pbs, task)
	w, err := harness.Load(o.workflow)
	if p == nil || err != nil {
		return false, 0, false
	}
	route, err := w.Route(task, "")
	if err != nil || route.How != "intent" {
		return false, 0, false
	}
	if !interactive {
		fmt.Fprintf(stderr, "error: both playbook %q and workflow %q match this task; pass --playbook %s or --workflow-name %s to choose\n", p.Name, route.Name, p.Name, route.Name)
		return false, 2, true
	}
	ans, err := playbookAsk(stdin, stderr, p.Name, route.Name)
	if err != nil {
		fmt.Fprintf(stderr, "error: no choice made; pass --playbook %s or --workflow-name %s\n", p.Name, route.Name)
		return false, 2, true
	}
	if ans == "w" {
		return false, 0, false
	}
	st.sel, st.decided = playbooks.Selection{Playbook: p, Method: playbooks.MethodKeywords, Keywords: words}, true
	o.pb.state = st
	o.workflow = ""
	return true, 0, false
}

// playbookPicker builds the one fallback call; tests replace it.
var playbookPicker = func(cfg config.AgentConfig, main string) (playbooks.Picker, error) {
	r := routing.Auxiliary(cfg, main, "playbook")
	m, err := routedModel(cfg, r, main, cfg.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, prompt string) (string, error) {
		msg, _, err := m.Query(ctx, []model.Message{{Role: "user", Content: prompt}}, nil)
		return msg.Content, err
	}, nil
}

// applyPlaybook routes the task (keywords, then the model fallback) and
// applies the chosen playbook to cfg: its checklist and skills join the
// system prompt, its done commands join verify_on_stop.checks, and
// read_only turns read-only mode on. handoff means the playbook names a
// workflow and the run was handed to it (code is its exit code).
func applyPlaybook(o *options, fs *flag.FlagSet, cfg *config.AgentConfig, task string, stdout, stderr io.Writer) (code int, handoff bool) {
	st := o.pb.state
	if st == nil {
		return 0, false
	}
	if !st.decided {
		var pick playbooks.Picker
		if st.fallback && len(st.pbs) > 0 {
			var err error
			if pick, err = playbookPicker(*cfg, o.modelName); err != nil {
				pick = func(context.Context, string) (string, error) { return "", err }
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		st.sel = playbooks.Select(ctx, st.pbs, task, pick)
		cancel()
	}
	fmt.Fprintln(stderr, st.sel.Line())
	p := st.sel.Playbook
	if p == nil {
		return 0, false
	}
	if p.Workflow != "" {
		if !o.trustConfig {
			fmt.Fprintf(stderr, "error: playbook %s hands off to workflow %s, which runs commands; pass --trust-config to allow it\n", p.Name, p.Workflow)
			return 2, true
		}
		path, err := cwdpath.Ref(st.root, p.Workflow)
		if err != nil {
			fmt.Fprintf(stderr, "error: playbook %s: workflow %s: %v\n", p.Name, p.Workflow, err)
			return 2, true
		}
		o.workflow, o.workflowName, o.task = path, "", task
		return runWorkflow(o, fs, os.Args[1:], stdout, stderr), true
	}
	skills, warns := p.LoadSkills(st.root)
	for _, w := range warns {
		fmt.Fprintln(stderr, "warning: "+w)
	}
	applyPlaybookConfig(cfg, p, skills)
	st.info = &trajectory.PlaybookInfo{Name: p.Name, Method: st.sel.Method, Keywords: st.sel.Keywords,
		Path: relTo(st.root, p.Path), Format: p.Format, Hash: playbooks.Hash(p, skills)}
	return 0, false
}

func applyPlaybookConfig(cfg *config.AgentConfig, p *playbooks.Playbook, skills []playbooks.Skill) {
	cfg.SystemPrompt += playbooks.Section(p, skills)
	cfg.VerifyOnStop.Checks = append(cfg.VerifyOnStop.Checks, p.Checks...)
	if p.ReadOnly {
		cfg.ReadOnly = true
	}
}

// playbookInfo is the run's playbook record (nil for none).
func playbookInfo(o *options) *trajectory.PlaybookInfo {
	if o.pb.state == nil {
		return nil
	}
	return o.pb.state.info
}

// emitPlaybook writes the playbook.selected event.
func emitPlaybook(info *trajectory.PlaybookInfo, emit func(string, map[string]any)) {
	if info == nil {
		return
	}
	f := map[string]any{"name": info.Name, "method": info.Method, "path": info.Path, "format": info.Format}
	if len(info.Keywords) > 0 {
		f["keywords"] = info.Keywords
	}
	emit("playbook.selected", f)
}

// resumePlaybook re-applies a checkpoint's playbook, refusing when the
// playbook or its skills changed since.
func resumePlaybook(cfg *config.AgentConfig, info *trajectory.PlaybookInfo, root string, stderr io.Writer) error {
	if info == nil {
		return nil
	}
	pbs, _ := playbooks.Discover(root)
	p, ok := playbooks.Get(pbs, info.Name)
	if !ok {
		return fmt.Errorf("this checkpoint followed playbook %q, which is no longer in ./playbooks", info.Name)
	}
	skills, warns := p.LoadSkills(root)
	if h := playbooks.Hash(p, skills); h != info.Hash {
		return fmt.Errorf("playbook %q (or a skill it loads) changed since this checkpoint; restore it or start a new run", info.Name)
	}
	for _, w := range warns {
		fmt.Fprintln(stderr, "warning: "+w)
	}
	fmt.Fprintf(stderr, "playbook: %s (from checkpoint)\n", info.Name)
	applyPlaybookConfig(cfg, p, skills)
	return nil
}

// checkPlaybooks is the --check row: the discovered playbooks, when
// routing is on or ./playbooks exists.
func checkPlaybooks(fs *flag.FlagSet, o options, cfg config.AgentConfig) string {
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return ""
	}
	on := playbooksOn(fs, &o, cfg)
	if _, err := os.Stat(playbooks.Dir(root)); err != nil && !on {
		return ""
	}
	pbs, warns := playbooks.Discover(root)
	state := "off"
	if on {
		state = "on"
	}
	v := fmt.Sprintf("%s, %d in ./playbooks", state, len(pbs))
	if len(pbs) > 0 {
		v += ": " + strings.Join(playbooks.Names(pbs), ", ")
	}
	if len(warns) > 0 {
		v += fmt.Sprintf(" (%d skipped with warnings)", len(warns))
	}
	return v
}

// chatPlaybook resolves /playbook <name> for the next chat turn.
func chatPlaybook(root, name string) (*playbooks.Playbook, error) {
	pbs, _ := playbooks.Discover(root)
	if p, ok := playbooks.Get(pbs, name); ok {
		if p.Workflow != "" {
			return nil, fmt.Errorf("playbook %s hands off to a workflow; run it with tinyagent --playbook %s --trust-config", name, name)
		}
		return p, nil
	}
	return nil, fmt.Errorf("no playbook %q in ./playbooks (have: %s)", name, strings.Join(playbooks.Names(pbs), ", "))
}

// chatPlaybookMessage prefixes msg with the playbook for one turn.
func chatPlaybookMessage(root string, p *playbooks.Playbook, msg string, warn func(string)) string {
	skills, warns := p.LoadSkills(root)
	for _, w := range warns {
		warn(w)
	}
	return "Follow this playbook for this request." + playbooks.Section(p, skills) + "\n\nRequest:\n" + msg
}
