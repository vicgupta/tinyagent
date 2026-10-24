// Command tinyagent is a minimal coding agent: an LLM with file and bash tools.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
	"github.com/vicgupta/tinyagent/internal/update"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version string

// flagOutput receives flag parse errors and the usage shown with them;
// helpOutput receives --help. Tests replace both.
var (
	flagOutput io.Writer = os.Stderr
	helpOutput io.Writer = os.Stdout
)

// options holds the parsed command line.
type options struct {
	task, modelName, configPath, cwd, output, resume string
	stepLimit, timeLimit                             int
	costLimit                                        float64
	verbose, noStream, noAgentsMD, noCompact         bool
	readOnly                                         bool
	jsonOutput                                       bool
	showHelp, showVersion, doUpdate                  bool
}

// newFlagSet defines the command-line flags, storing their values in o. Each
// long name also works with a single dash (-task), as with any Go flag.
func newFlagSet(o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("tinyagent", flag.ContinueOnError)
	fs.SetOutput(flagOutput)
	fs.Usage = func() { printUsage(fs.Output(), fs) }
	fs.BoolVar(&o.showHelp, "help", false, "show this help and exit")
	fs.BoolVar(&o.showHelp, "h", false, "shorthand for --help")
	fs.StringVar(&o.task, "task", "", `task/prompt (required unless --resume); "-" reads it from stdin`)
	fs.StringVar(&o.task, "t", "", "shorthand for --task")
	fs.StringVar(&o.modelName, "model", defaultModel(), "provider/model; if unset, $AGENT_MODEL, then the config's model")
	fs.StringVar(&o.modelName, "m", defaultModel(), "shorthand for --model")
	fs.StringVar(&o.configPath, "config", "", "YAML config (default: user config if present, else built-in)")
	fs.StringVar(&o.configPath, "c", "", "shorthand for --config")
	fs.StringVar(&o.cwd, "cwd", ".", "working directory")
	fs.IntVar(&o.stepLimit, "step-limit", 0, "max model calls, 0 = unlimited")
	fs.Float64Var(&o.costLimit, "cost-limit", 0, "max USD, 0 = unlimited")
	fs.IntVar(&o.timeLimit, "time-limit", 0, "max wall-clock seconds, 0 = unlimited")
	fs.StringVar(&o.output, "output", "", "write trajectory JSON")
	fs.StringVar(&o.output, "o", "", "shorthand for --output")
	fs.StringVar(&o.resume, "resume", "", "continue an unfinished run from a trajectory JSON checkpoint")
	fs.BoolVar(&o.verbose, "verbose", false, "print tool outputs")
	fs.BoolVar(&o.verbose, "v", false, "shorthand for --verbose")
	fs.BoolVar(&o.noStream, "no-stream", false, "wait for full responses instead of streaming")
	fs.BoolVar(&o.noAgentsMD, "no-agents-md", false, "don't add AGENTS.md (nearest, up to the repo root) to the system prompt")
	fs.BoolVar(&o.noCompact, "no-compact", false, "don't compact the history when it nears the context window")
	fs.BoolVar(&o.readOnly, "read-only", false, "allow only read_file, list_files, and grep; blocks edits and commands")
	fs.BoolVar(&o.jsonOutput, "json", false, "write run events as JSONL to stdout")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&o.showVersion, "V", false, "shorthand for --version")
	fs.BoolVar(&o.doUpdate, "update", false, "update to the latest release and exit")
	return fs
}

func main() {
	os.Exit(run())
}

func run() int {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if o.showHelp {
		printUsage(helpOutput, fs)
		return 0
	}
	// Parsing stops at the first non-flag, so any flags after it would be
	// silently ignored.
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "error: unexpected positional argument %q; pass the task with -t/--task\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	if msg := validateLimits(o); msg != "" {
		fmt.Fprintln(fs.Output(), "error: "+msg)
		return 2
	}
	if o.showVersion {
		fmt.Println("tinyagent", currentVersion())
		return 0
	}
	if o.doUpdate {
		u, err := update.New(currentVersion())
		if err != nil {
			return fail(err)
		}
		msg, err := u.Run(context.Background())
		if err != nil {
			return fail(err)
		}
		fmt.Println(msg)
		return 0
	}
	if o.resume != "" {
		return runResume(&o, fs)
	}
	if o.task == "" {
		fmt.Fprintln(os.Stderr, "error: --task is required")
		fs.Usage()
		return 2
	}
	task, err := readTask(o.task, os.Stdin)
	if err != nil {
		return fail(err)
	}

	cfg, err := config.Load(config.Find(o.configPath))
	if err != nil {
		return fail(err)
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "read-only") {
		cfg.ReadOnly = o.readOnly
	}
	o.modelName = resolveModel(fs, o.modelName, cfg.Model)
	model.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}

	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return fail(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("--cwd %s is not a directory", o.cwd))
	}
	if cfg.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	started := time.Now()
	if cfg.WallTimeSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.WallTimeSec)*time.Second)
		defer cancel()
	}
	mcpRegistry, err := mcptools.Start(ctx, root, cfg.MCPServers, os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer mcpRegistry.Close()

	var onText func(string)
	if !o.noStream && !o.jsonOutput {
		onText = func(s string) { os.Stdout.WriteString(s) }
	}
	m, err := model.New(o.modelName, onText)
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, o.modelName, err.Error())
		return code
	}
	warnUnpriced(os.Stderr, o.modelName, cfg.CostLimit)

	a := &agent.Agent{
		Model:     m,
		ModelName: o.modelName,
		Tools: &tools.Executor{
			Root:           root,
			ReadOnly:       cfg.ReadOnly,
			BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars: cfg.MaxOutputChars,
			MaxProcesses:   cfg.MaxBackgroundProcesses,
			Stream:         jsonStream(o.jsonOutput),
			ExternalSpecs:  mcpRegistry.Specs,
			ExternalCall:   mcpRegistry.Call,
		},
		Config:       cfg,
		Out:          runOutput(o.jsonOutput),
		Err:          os.Stderr,
		Verbose:      o.verbose,
		Streamed:     !o.noStream,
		SkipAgentsMD: o.noAgentsMD,
		NoCompact:    o.noCompact,
	}
	events := newJSONEvents(os.Stdout, o.jsonOutput)
	if o.jsonOutput {
		events.emit("run.started", map[string]any{"schema_version": 1, "model": o.modelName, "root": root, "read_only": cfg.ReadOnly, "resumed": false})
		a.OnEvent = func(kind string, fields map[string]any) {
			events.emit(kind, fields)
			if events.err != nil {
				stop()
			}
		}
		if cfg.CostLimit > 0 && model.Unpriced(o.modelName) {
			events.emit("warning", map[string]any{"message": fmt.Sprintf("no pricing available for %s; --cost-limit cannot be enforced", o.modelName)})
		}
	}
	if events.err != nil {
		return 1
	}
	if o.output != "" {
		a.OnStep = func(t *trajectory.Trajectory) error {
			if err := t.Save(o.output); err != nil {
				fmt.Fprintln(os.Stderr, "warning: saving trajectory:", err)
				if a.OnEvent != nil {
					a.OnEvent("warning", map[string]any{"message": "saving trajectory: " + err.Error()})
				}
			}
			return nil
		}
	}

	if cfg.WallTimeSec > 0 {
		remaining := time.Duration(cfg.WallTimeSec)*time.Second - time.Since(started)
		if remaining <= 0 {
			return fail(errors.New("wall-clock time limit expired during MCP discovery"))
		}
		cfg.WallTimeSec = int(remaining / time.Second)
		if cfg.WallTimeSec == 0 {
			cfg.WallTimeSec = 1
		}
		a.Config.WallTimeSec = cfg.WallTimeSec
	}
	runStart := time.Now()
	t := a.Run(ctx, task)
	elapsed := time.Since(runStart)
	reportSummary(os.Stderr, o.modelName, t, elapsed)
	if o.jsonOutput {
		if t.ExitStatus == agent.StatusError {
			events.emit("error", map[string]any{"message": t.Submission})
		}
		events.emit("result", resultFields(t, elapsed))
		if events.err != nil {
			return 1
		}
	}
	if t.ExitStatus == agent.StatusSubmitted || t.ExitStatus == agent.StatusCompleted {
		return 0
	}
	return 1
}

// runResume continues an unfinished run from a validated checkpoint. It reuses
// the saved task and model, requires a matching working directory and a
// distinct output destination, and refuses to continue if the effective
// instructions or tool set have changed.
func runResume(o *options, fs *flag.FlagSet) int {
	if flagWasSet(fs, "task", "t") {
		fmt.Fprintln(os.Stderr, "error: --resume and --task are mutually exclusive")
		return 2
	}

	if !flagWasSet(fs, "cwd") {
		return fail(errors.New("resume: an explicit --cwd matching the checkpoint root is required"))
	}

	src, err := trajectory.Load(o.resume)
	if err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}

	// The saved model wins unless an explicit, different model is given.
	if flagWasSet(fs, "model", "m") && o.modelName != src.Model {
		return fail(fmt.Errorf("resume: --model %q conflicts with the checkpoint's model %q", o.modelName, src.Model))
	}
	modelName := src.Model

	cfg, err := config.Load(config.Find(o.configPath))
	if err != nil {
		return fail(err)
	}
	if len(cfg.MCPServers) > 0 || src.MCPEnabled || len(src.ExternalTools) > 0 {
		return fail(errors.New("resume: MCP-enabled runs cannot be resumed, and MCP configuration cannot be added to a built-in checkpoint; start a fresh task"))
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "read-only") && o.readOnly != src.ReadOnly {
		return fail(errors.New("resume: --read-only conflicts with the checkpoint's recorded mode; start a fresh task"))
	}
	cfg.ReadOnly = src.ReadOnly
	model.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}

	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return fail(err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("--cwd %s is not a directory", o.cwd))
	}
	if root != src.Root {
		return fail(fmt.Errorf("resume: --cwd %s does not match the checkpoint's root %s", root, src.Root))
	}

	if o.output == "" {
		return fail(errors.New("resume: --output is required to write the continued trajectory"))
	}
	if sameFile(o.resume, o.output) {
		return fail(errors.New("resume: --output must be distinct from the source checkpoint"))
	}
	if _, err := os.Lstat(o.output); err == nil {
		return fail(errors.New("resume: --output already exists; choose a new destination"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("resume: checking --output: %w", err))
	}
	if flagWasSet(fs, "no-agents-md") && o.noAgentsMD != src.SkipAgentsMD {
		return fail(errors.New("resume: --no-agents-md conflicts with the recorded loading mode; start a fresh task"))
	}
	if err := agent.ValidateResume(src, cfg, root); err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}
	if src.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}

	var onText func(string)
	if !o.noStream && !o.jsonOutput {
		onText = func(s string) { os.Stdout.WriteString(s) }
	}
	m, err := model.New(modelName, onText)
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, modelName, err.Error())
		return code
	}
	warnUnpriced(os.Stderr, modelName, cfg.CostLimit)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The agent records this boundary and clears the active termination before
	// persisting the new starting state.
	prevStatus := src.ExitStatus
	if prevStatus == "" {
		prevStatus = "in progress"
	}

	a := &agent.Agent{
		Model:     m,
		ModelName: modelName,
		Tools: &tools.Executor{
			Root:           root,
			ReadOnly:       src.ReadOnly,
			BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars: cfg.MaxOutputChars,
			MaxProcesses:   cfg.MaxBackgroundProcesses,
			Stream:         jsonStream(o.jsonOutput),
		},
		Config:       cfg,
		Out:          runOutput(o.jsonOutput),
		Err:          os.Stderr,
		Verbose:      o.verbose,
		Streamed:     !o.noStream,
		SkipAgentsMD: src.SkipAgentsMD,
		NoCompact:    o.noCompact,
		Resume:       src,
		ResumeSource: o.resume,
	}
	events := newJSONEvents(os.Stdout, o.jsonOutput)
	if o.jsonOutput {
		events.emit("run.started", map[string]any{"schema_version": 1, "model": modelName, "root": root, "read_only": src.ReadOnly, "resumed": true})
		a.OnEvent = func(kind string, fields map[string]any) {
			events.emit(kind, fields)
			if events.err != nil {
				stop()
			}
		}
		if cfg.CostLimit > 0 && model.Unpriced(modelName) {
			events.emit("warning", map[string]any{"message": fmt.Sprintf("no pricing available for %s; --cost-limit cannot be enforced", modelName)})
		}
	}
	if events.err != nil {
		return 1
	}
	initial := true
	a.OnStep = func(t *trajectory.Trajectory) error {
		if initial {
			if err := t.SaveNew(o.output); err != nil {
				return err
			}
			initial = false
			return nil
		}
		return t.Save(o.output)
	}

	fmt.Fprintf(os.Stderr, "resuming %s from %s (previous status %s after %d steps)\n", modelName, o.resume, prevStatus, src.Steps)
	fmt.Fprintln(os.Stderr, "note: inspect and revalidate the workspace; it may have changed and commands may have produced unrecorded side effects after the last save. Live processes are not restored")

	started := time.Now()
	t := a.Run(ctx, src.Task)
	elapsed := time.Since(started)
	reportSummary(os.Stderr, modelName, t, elapsed)
	if o.jsonOutput {
		if t.ExitStatus == agent.StatusError {
			events.emit("error", map[string]any{"message": t.Submission})
		}
		events.emit("result", resultFields(t, elapsed))
		if events.err != nil {
			return 1
		}
	}
	if t.ExitStatus == agent.StatusSubmitted || t.ExitStatus == agent.StatusCompleted {
		return 0
	}
	return 1
}

// sameFile reports whether two paths refer to the same file, following
// symlinks and detecting hard links.
func sameFile(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA == nil && errB == nil && os.SameFile(ai, bi) {
		return true
	}
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	if ra != "" && rb != "" && ra == rb {
		return true
	}
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func flagWasSet(fs *flag.FlagSet, names ...string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		for _, name := range names {
			if f.Name == name {
				found = true
			}
		}
	})
	return found
}

func canonicalRoot(path string) (string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

// usageGroups lays out --help: each flag's shorthand, long name and value
// placeholder, by group. Help text and defaults come from the FlagSet.
var usageGroups = []struct {
	name  string
	flags [][3]string
}{
	{"", [][3]string{{"h", "help", ""}}},
	{"Task", [][3]string{{"t", "task", "string"}, {"m", "model", "string"}, {"c", "config", "path"}, {"", "cwd", "dir"}, {"", "resume", "path"}}},
	{"Limits", [][3]string{{"", "step-limit", "n"}, {"", "cost-limit", "usd"}, {"", "time-limit", "sec"}, {"", "no-compact", ""}}},
	{"Output", [][3]string{{"o", "output", "path"}, {"v", "verbose", ""}, {"", "no-stream", ""}, {"", "json", ""}, {"", "no-agents-md", ""}, {"", "read-only", ""}}},
	{"Other", [][3]string{{"V", "version", ""}, {"", "update", ""}}},
}

func printUsage(w io.Writer, fs *flag.FlagSet) {
	left := func(f [3]string) string {
		s := "      --" + f[1]
		if f[0] != "" {
			s = "  -" + f[0] + ", --" + f[1]
		}
		if f[2] != "" {
			s += " " + f[2]
		}
		return s
	}
	width := 0
	for _, g := range usageGroups {
		for _, f := range g.flags {
			width = max(width, len(left(f)))
		}
	}
	fmt.Fprint(w, "Usage: tinyagent -t \"<prompt>\" [flags]    (or --task \"<prompt>\")\n       echo \"<prompt>\" | tinyagent -t -\n       tinyagent --resume <trajectory.json> --cwd <dir> -o <new.json> [flags]\n\n")
	for _, g := range usageGroups {
		if g.name != "" {
			fmt.Fprintf(w, "%s:\n", g.name)
		}
		for _, f := range g.flags {
			fl := fs.Lookup(f[1])
			help := fl.Usage
			if d := fl.DefValue; d != "" && d != "0" && d != "false" {
				help += fmt.Sprintf(" (default %q)", d)
			}
			fmt.Fprintf(w, "%-*s%s\n", width+4, left(f), help)
		}
	}
	fmt.Fprint(w, "\nExamples:\n"+
		"  tinyagent -t \"fix the failing test\" -m anthropic/claude-sonnet-4\n"+
		"  cat task.md | tinyagent -t - --step-limit 30 -o run.json\n"+
		"  tinyagent --resume run.json --cwd . --step-limit 60 -o continued.json\n")
}

// reportSummary prints one line on how the run ended: status, steps, elapsed
// time, and token and cost totals when known. For an error that looks like a
// setup problem, a hint line follows.
func reportSummary(w io.Writer, modelName string, t *trajectory.Trajectory, elapsed time.Duration) {
	fmt.Fprintln(w, summaryLine(t, elapsed))
	if t.ExitStatus == agent.StatusError {
		printHint(w, modelName, t.Submission)
	}
}

func summaryLine(t *trajectory.Trajectory, elapsed time.Duration) string {
	s := fmt.Sprintf("%s after %d steps in %s", t.ExitStatus, t.Steps, formatDuration(elapsed))
	if t.InputTokens > 0 || t.OutputTokens > 0 || t.Cost > 0 {
		s += fmt.Sprintf(" (%s input / %s output tokens", formatInt(t.InputTokens), formatInt(t.OutputTokens))
		if t.CacheReadTokens > 0 {
			s += ", " + formatInt(t.CacheReadTokens) + " cached"
		}
		if t.Cost > 0 {
			s += ", " + formatCost(t.Cost)
		}
		s += ")"
	}
	if t.ExitStatus == agent.StatusError {
		s += ": " + t.Submission
	}
	return s
}

// formatDuration renders d as 12s, 1m42s or 1h02m.
func formatDuration(d time.Duration) string {
	sec := int(d.Round(time.Second) / time.Second)
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh%02dm", sec/3600, sec%3600/60)
}

// formatCost uses 2 decimals from one cent up and 4 below it.
func formatCost(usd float64) string {
	if usd >= 0.01 {
		return fmt.Sprintf("$%.2f", usd)
	}
	return fmt.Sprintf("$%.4f", usd)
}

// formatInt adds thousands separators: 48213 -> 48,213.
func formatInt(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + formatInt(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func printHint(w io.Writer, modelName, errMsg string) {
	if h := model.Hint(modelName, errMsg); h != "" {
		fmt.Fprintln(w, "hint:", h)
	}
}

// warnUnpriced warns when a cost limit is set but the model's cost can't be
// estimated, so the limit would never stop the run.
func warnUnpriced(w io.Writer, modelName string, costLimit float64) {
	if costLimit > 0 && model.Unpriced(modelName) {
		fmt.Fprintf(w, "warning: no pricing available for %s; --cost-limit cannot be enforced. Set input_price_per_mtok and output_price_per_mtok in your config to enable cost tracking.\n", modelName)
	}
}

// validateLimits reports the first limit flag a run can't honor, or "" if
// they're all fine. The agent ignores limits <= 0, so a negative or NaN value
// would silently mean unlimited.
func validateLimits(o options) string {
	switch {
	case o.stepLimit < 0:
		return "--step-limit must be >= 0 (0 = unlimited)"
	case !(o.costLimit >= 0) || math.IsInf(o.costLimit, 1):
		return "--cost-limit must be finite and >= 0 (0 = unlimited)"
	case o.timeLimit < 0:
		return "--time-limit must be >= 0"
	}
	return ""
}

// applyFlagOverrides copies limit flags into cfg, but only the ones given
// explicitly, so unset flags leave the config file's values alone.
func applyFlagOverrides(fs *flag.FlagSet, cfg *config.AgentConfig, stepLimit int, costLimit float64, timeLimit int) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "step-limit":
			cfg.StepLimit = stepLimit
		case "cost-limit":
			cfg.CostLimit = costLimit
		case "time-limit":
			cfg.WallTimeSec = timeLimit
		}
	})
}

// resolveModel picks the model: an explicit -m/--model, else $AGENT_MODEL,
// else the config's model, else the built-in default.
func resolveModel(fs *flag.FlagSet, flagModel, cfgModel string) string {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "model" || f.Name == "m" {
			explicit = true
		}
	})
	switch {
	case explicit:
		return flagModel
	case os.Getenv("AGENT_MODEL") != "":
		return os.Getenv("AGENT_MODEL")
	case cfgModel != "":
		return cfgModel
	}
	return builtinModel
}

// currentVersion prefers the -ldflags stamp, then the module version that
// `go install pkg@vX` records, then "dev".
func currentVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(bi.Main.Version, "v") {
		return bi.Main.Version
	}
	return "dev"
}

// readTask returns task, or the trimmed contents of stdin when task is "-".
func readTask(task string, stdin io.Reader) (string, error) {
	if task != "-" {
		return task, nil
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading task from stdin: %w", err)
	}
	if task = strings.TrimSpace(string(b)); task == "" {
		return "", errors.New("--task - got an empty task on stdin")
	}
	return task, nil
}

const builtinModel = "openai/gpt-4o"

// defaultModel is the --model default shown in help: $AGENT_MODEL, else the
// built-in model. resolveModel applies the config's model in between.
func defaultModel() string {
	if v := os.Getenv("AGENT_MODEL"); v != "" {
		return v
	}
	return builtinModel
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

type jsonEvents struct {
	out     io.Writer
	enabled bool
	err     error
}

func newJSONEvents(out io.Writer, enabled bool) *jsonEvents {
	return &jsonEvents{out: out, enabled: enabled}
}

func (e *jsonEvents) emit(kind string, fields map[string]any) {
	if !e.enabled || e.err != nil {
		return
	}
	event := make(map[string]any, len(fields)+1)
	event["type"] = kind
	for key, value := range fields {
		event[key] = value
	}
	line, err := json.Marshal(event)
	if err != nil {
		e.err = err
		return
	}
	line = append(line, '\n')
	n, err := e.out.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	e.err = err
}

func runOutput(jsonMode bool) io.Writer {
	if jsonMode {
		return io.Discard
	}
	return os.Stdout
}

func jsonStream(jsonMode bool) io.Writer {
	if jsonMode {
		return nil
	}
	return os.Stdout
}

func resultFields(t *trajectory.Trajectory, elapsed time.Duration) map[string]any {
	return map[string]any{
		"status": t.ExitStatus, "submission": t.Submission, "steps": t.Steps,
		"input_tokens": t.InputTokens, "output_tokens": t.OutputTokens,
		"cache_read_tokens": t.CacheReadTokens, "cache_write_tokens": t.CacheWriteTokens,
		"cost_usd": t.Cost, "duration_ms": elapsed.Milliseconds(),
	}
}
