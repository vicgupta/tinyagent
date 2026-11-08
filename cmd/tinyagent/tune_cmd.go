package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/routing"
	"github.com/vicgupta/tinyagent/internal/selftune"
)

// tuneFlags hold the opt-in self-tuning flags (issue #390).
type tuneFlags struct {
	propose, list, infer             bool
	runs                             stringList
	workflow, show, validate, digest string
	apply, revert                    string
}

// tuneCommandFlags are the tune commands; exactly one may be set.
var tuneCommandFlags = []string{"tune-propose", "tune-list", "tune-show", "tune-validate", "tune-apply", "tune-revert"}

// tuneOptionFlags only apply to one tune command.
var tuneOptionFlags = map[string]string{"tune-run": "tune-propose", "tune-workflow": "tune-propose", "tune-infer": "tune-propose", "tune-digest": "tune-apply"}

// tuneAllowedFlags are the other flags a tune command accepts.
var tuneAllowedFlags = map[string]bool{"cwd": true, "json": true, "config": true, "c": true, "profile": true, "trust-config": true,
	"model": true, "m": true, "aux-model": true, "route": true, "no-fallback": true, "verbose": true, "v": true}

// tuneGroup lists the tune flags in --help --verbose.
var tuneGroup = flagGroup{"Self-tuning (opt-in)", [][3]string{
	{"", "tune-propose", ""}, {"", "tune-run", "path"}, {"", "tune-workflow", "file.yaml"}, {"", "tune-infer", ""},
	{"", "tune-list", ""}, {"", "tune-show", "id"}, {"", "tune-validate", "id"}, {"", "tune-apply", "id"}, {"", "tune-digest", "digest"}, {"", "tune-revert", "id"},
}}

func registerTuneFlags(fs *flag.FlagSet, t *tuneFlags) {
	fs.BoolVar(&t.propose, "tune-propose", false, "self-tuning (needs self_tuning.enabled): from the --tune-run records, propose one skill or success-check change for the failure shared by at least 2 distinct failed runs, write it to .tinyagent/observer/candidates/ in --cwd and exit; nothing is applied")
	fs.Var(&t.runs, "tune-run", "with --tune-propose: a --workflow -o record inside --cwd to learn from (repeatable; passed runs are rerun as regression controls)")
	fs.StringVar(&t.workflow, "tune-workflow", "", "with --tune-propose: the workflow file (inside --cwd) the runs used; the changed file is the failed stage's skill in it")
	fs.BoolVar(&t.infer, "tune-infer", false, "with --tune-propose: phrase the change with the auxiliary model (task tuning) within self_tuning.max_tokens, max_cost_usd and max_attempts (same as self_tuning.inference)")
	fs.BoolVar(&t.list, "tune-list", false, "print the self-tuning candidates in --cwd with status and validation result (--json for a JSON array), then exit")
	fs.StringVar(&t.show, "tune-show", "", "print one self-tuning candidate: evidence, rationale, expected effect, diff, validation and its approval digest (--json for the full record), then exit")
	fs.StringVar(&t.validate, "tune-validate", "", "rerun the candidate's failed and control tasks with and without the change, each in a fresh copy of --cwd, within self_tuning.validation_runs and validation_cost_usd; a regression or no improvement rejects it")
	fs.StringVar(&t.apply, "tune-apply", "", "apply this validated candidate's exact change; needs --tune-digest with the digest shown by --tune-show (explicit approval)")
	fs.StringVar(&t.digest, "tune-digest", "", "with --tune-apply: the approved candidate's digest from --tune-show")
	fs.StringVar(&t.revert, "tune-revert", "", "restore the file an applied candidate changed, if it still holds exactly that change")
}

// tuneCommandSet reports whether a tune flag was given.
func tuneCommandSet(fs *flag.FlagSet) bool {
	if flagWasSet(fs, tuneCommandFlags...) {
		return true
	}
	for f := range tuneOptionFlags {
		if flagWasSet(fs, f) {
			return true
		}
	}
	return false
}

// tuneNow is the clock; tests replace it.
var tuneNow = time.Now

// tuneModel builds the auxiliary tuning model; tests replace it.
var tuneModel = func(cfg config.AgentConfig, fs *flag.FlagSet, o *options) (model.Model, string, error) {
	main := resolveModel(fs, o.modelName, cfg.Model)
	r := routing.Auxiliary(cfg, main, routing.TaskTuning)
	m, err := routedModel(cfg, r, main, "")
	if err != nil {
		return nil, "", err
	}
	if model.MaxOutputTokensSupported(r.Model) == nil {
		_ = model.SetMaxOutputTokens(m, r.Model, cfg.SelfTuning.EffMaxTokens())
	}
	return m, r.Model, nil
}

// runTuneCommand implements the tune flags. Usage errors and self-tuning
// being off exit 2; a failed proposal, validation or apply exits 1.
func runTuneCommand(o *options, fs *flag.FlagSet, stdout, stderr io.Writer) int {
	var set []string
	for _, n := range tuneCommandFlags {
		if flagWasSet(fs, n) {
			set = append(set, n)
		}
	}
	if len(set) != 1 {
		if len(set) == 0 {
			fmt.Fprintln(stderr, "error: --tune-run, --tune-workflow and --tune-infer apply only to --tune-propose, --tune-digest only to --tune-apply")
		} else {
			fmt.Fprintf(stderr, "error: --%s can't be combined; run one tune command at a time\n", strings.Join(set, " and --"))
		}
		return 2
	}
	cmd := set[0]
	var bad string
	fs.Visit(func(f *flag.Flag) {
		if bad != "" || f.Name == cmd || tuneAllowedFlags[f.Name] {
			return
		}
		if owner, ok := tuneOptionFlags[f.Name]; ok && owner == cmd {
			return
		}
		bad = f.Name
	})
	if bad != "" {
		fmt.Fprintf(stderr, "error: --%s only manages self-tuning candidates; it can't be combined with -%s\n", cmd, dashName(bad))
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected positional argument %q\n", fs.Arg(0))
		return 2
	}
	if fi, err := os.Stat(o.cwd); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "error: --cwd %q is not a directory\n", o.cwd)
		return 2
	}
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: tune:", err)
		return 1
	}
	_, cfg, err := loadConfig(o, root, stderr)
	if err != nil {
		return loadConfigError(err)
	}
	st := cfg.SelfTuning
	if !st.Enabled {
		fmt.Fprintln(stderr, "error:", selftune.ErrOff)
		return 2
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	var jl *eventlog.JSONL
	if o.jsonOutput {
		jl = eventlog.NewJSONL(stdout)
	}
	emit := func(kind string, f map[string]any) {
		if jl != nil {
			jl.Emit(kind, f)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	t := o.tune
	switch cmd {
	case "tune-list":
		return tuneList(root, o.jsonOutput, stdout, stderr)
	case "tune-show":
		return tuneShow(root, strings.TrimSpace(t.show), o.jsonOutput, stdout, stderr)
	case "tune-propose":
		po := selftune.ProposeOptions{Root: root, Workflow: t.workflow, Runs: t.runs, Settings: st, Redact: eventlog.EnvRedactor().Redact, Now: tuneNow()}
		if t.infer || st.Inference {
			m, name, err := tuneModel(cfg, fs, o)
			if err != nil {
				fmt.Fprintln(stderr, "error: tune: auxiliary model (task tuning):", err)
				return 1
			}
			po.Model, po.ModelName = m, name
		}
		p, err := selftune.Propose(ctx, po)
		if err != nil {
			fmt.Fprintln(stderr, "error: tune:", err)
			return 1
		}
		for _, w := range p.Warnings {
			fmt.Fprintln(stderr, "warning:", w)
		}
		if r := p.Recurrence; r != nil {
			fmt.Fprintf(stderr, "warning: tune: gate did not hold: the failure of stage %s recurred after candidate %s was applied to %s; no new candidate (revert it with tinyagent tune revert %s, or edit the skill by hand)\n", r.Stage, r.ID, r.Target, r.ID)
			emit("tune.recurrence", map[string]any{"id": r.ID, "signature": r.Signature, "stage": r.Stage, "target": r.Target})
			return 0
		}
		c := p.Candidate
		verb := "proposed"
		if p.Existing {
			verb = "already proposed"
		}
		if !o.jsonOutput {
			fmt.Fprintf(stdout, "%s %s: %s change to %s for stage %s from %d failed runs\nreview: tinyagent tune show %s\nvalidate: tinyagent tune validate %s\n", verb, c.ID, c.Kind, c.Target, c.Stage, len(c.Evidence), c.ID, c.ID)
		}
		f := map[string]any{"id": c.ID, "kind": c.Kind, "stage": c.Stage, "target": c.Target, "signature": c.Signature, "digest": c.Digest, "evidence": len(c.Evidence), "controls": len(c.Controls), "existing": p.Existing}
		if c.Inference != nil {
			f["inference"] = c.Inference
		}
		emit("tune.proposed", f)
		return 0
	case "tune-validate":
		agent, err := runWorkflowAgent()
		if err != nil {
			return fail(err)
		}
		c, err := selftune.Validate(ctx, selftune.ValidateOptions{Root: root, ID: strings.TrimSpace(t.validate), Settings: st, RunAgent: agent, Progress: stderr, Now: tuneNow()})
		if err != nil {
			fmt.Fprintln(stderr, "error: tune:", err)
			return 1
		}
		v := c.Validation
		if !o.jsonOutput {
			fmt.Fprintf(stdout, "%s %s: %s\n", c.ID, v.Verdict, v.Reason)
			if c.Status == selftune.StatusValidated {
				fmt.Fprintf(stdout, "review, then approve this exact change with:\n  tinyagent tune apply %s --digest %s\n", c.ID, c.Digest)
			}
		}
		emit("tune.validated", map[string]any{"id": c.ID, "status": c.Status, "verdict": v.Verdict, "reason": v.Reason, "runs": v.Runs, "cost_usd": v.CostUSD, "improvements": v.Improvements, "regressions": v.Regressions, "outcomes": v.Outcomes})
		if c.Status != selftune.StatusValidated {
			return 1
		}
		return 0
	case "tune-apply":
		c, err := selftune.Apply(st, root, strings.TrimSpace(t.apply), t.digest, tuneNow())
		if err != nil {
			fmt.Fprintln(stderr, "error: tune:", err)
			return 1
		}
		if !o.jsonOutput {
			fmt.Fprintf(stdout, "applied %s to %s (future fresh runs only); undo with: tinyagent tune revert %s\n", c.ID, c.Target, c.ID)
		}
		emit("tune.applied", map[string]any{"id": c.ID, "target": c.Target, "digest": c.Digest})
		return 0
	}
	c, err := selftune.Revert(st, root, strings.TrimSpace(t.revert), tuneNow())
	if err != nil {
		fmt.Fprintln(stderr, "error: tune:", err)
		return 1
	}
	if !o.jsonOutput {
		fmt.Fprintf(stdout, "reverted %s: restored %s\n", c.ID, c.Target)
	}
	emit("tune.reverted", map[string]any{"id": c.ID, "target": c.Target})
	return 0
}

func tuneList(root string, asJSON bool, stdout, stderr io.Writer) int {
	cs, warns, err := selftune.List(root)
	if err != nil {
		fmt.Fprintln(stderr, "error: tune:", err)
		return 1
	}
	for _, w := range warns {
		fmt.Fprintln(stderr, "warning: tune:", w)
	}
	if asJSON {
		out := []map[string]any{}
		for _, c := range cs {
			m := map[string]any{"id": c.ID, "status": c.Status, "kind": c.Kind, "stage": c.Stage, "target": c.Target, "created": c.Created, "digest": c.Digest}
			if c.Validation != nil {
				m["verdict"] = c.Validation.Verdict
			}
			out = append(out, m)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if enc.Encode(out) != nil {
			return 1
		}
		return 0
	}
	if len(cs) == 0 {
		fmt.Fprintf(stdout, "no self-tuning candidates (%s)\n", selftune.RelDir)
		return 0
	}
	for _, c := range cs {
		fmt.Fprintln(stdout, c.Summary())
	}
	return 0
}

func tuneShow(root, id string, asJSON bool, stdout, stderr io.Writer) int {
	c, err := selftune.Load(root, id)
	if err != nil {
		fmt.Fprintln(stderr, "error: tune:", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if enc.Encode(c) != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "candidate %s (%s, %s change)\nstage %s of workflow %s (%s)\ntarget %s\ndigest %s\n\n", c.ID, c.Status, c.Kind, c.Stage, c.Workflow, c.WorkflowFile, c.Target, c.Digest)
	fmt.Fprintf(stdout, "rationale: %s\nexpected effect: %s\n\nevidence:\n", c.Rationale, c.ExpectedEffect)
	for _, e := range c.Evidence {
		fmt.Fprintf(stdout, "  %s  %s at %s: %s\n", e.Path, e.Status, e.Stage, e.Reason)
	}
	for _, e := range c.Controls {
		fmt.Fprintf(stdout, "  %s  passed (control)\n", e.Path)
	}
	if ob := c.Observer; ob != nil {
		fmt.Fprintf(stdout, "observer: proposal %s, seen %d time(s) since %s", ob.ArtifactID, ob.Count, ob.FirstSeen)
		if ob.RuleID != "" {
			fmt.Fprintf(stdout, ", ratchet rule %s applied", ob.RuleID)
		}
		fmt.Fprintln(stdout)
	}
	if in := c.Inference; in != nil {
		fmt.Fprintf(stdout, "inference: %s via %s (%d attempt(s), $%.4f)", in.Status, in.Model, in.Attempts, in.CostUSD)
		if in.Reason != "" {
			fmt.Fprintf(stdout, ": %s", in.Reason)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "\n%s\n", c.Diff)
	if v := c.Validation; v != nil {
		fmt.Fprintf(stdout, "validation: %s: %s (%d runs, $%.4f)\n", v.Verdict, v.Reason, v.Runs, v.CostUSD)
		for _, x := range v.Outcomes {
			fmt.Fprintf(stdout, "  %-9s task %s (%s): %s\n", x.Variant, x.Task, x.Source, x.Status)
		}
	} else {
		fmt.Fprintf(stdout, "not validated yet: tinyagent tune validate %s\n", c.ID)
	}
	if c.Status == selftune.StatusValidated {
		fmt.Fprintf(stdout, "approve this exact change with: tinyagent tune apply %s --digest %s\n", c.ID, c.Digest)
	}
	return 0
}
