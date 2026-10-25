// Command tinyagent is a minimal coding agent: an LLM with file and bash tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/skills"
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
	outputSchema                                     string
	planFile                                         string
	plan                                             bool
	skills, noSkills                                 bool
	reasoningEffort                                  string
	openaiAPI                                        string
	execEnv                                          string
	stepLimit, timeLimit, maxFormatErrors            int
	costLimit                                        float64
	verbose, noStream, noAgentsMD, noCompact         bool
	readOnly, sandbox, sandboxNetwork                bool
	jsonOutput                                       bool
	logDB                                            optionalPath
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
	fs.IntVar(&o.maxFormatErrors, "max-format-errors", config.DefaultMaxFormatErrors, "stop after this many consecutive steps of malformed tool calls (>= 1)")
	fs.StringVar(&o.output, "output", "", "write trajectory JSON")
	fs.StringVar(&o.output, "o", "", "shorthand for --output")
	fs.StringVar(&o.resume, "resume", "", "continue an unfinished run from a trajectory JSON checkpoint")
	fs.BoolVar(&o.verbose, "verbose", false, "print tool outputs")
	fs.BoolVar(&o.verbose, "v", false, "shorthand for --verbose")
	fs.BoolVar(&o.noStream, "no-stream", false, "wait for full responses instead of streaming")
	fs.BoolVar(&o.noAgentsMD, "no-agents-md", false, "don't add AGENTS.md (nearest, up to the repo root) to the system prompt")
	fs.BoolVar(&o.noCompact, "no-compact", false, "don't compact the history when it nears the context window")
	fs.BoolVar(&o.readOnly, "read-only", false, "allow only read_file, list_files, and grep; blocks edits and commands")
	fs.BoolVar(&o.sandbox, "sandbox", false, "confine bash and start_process commands: writes only under --cwd, a temp dir and sandbox_writable_paths")
	fs.BoolVar(&o.sandboxNetwork, "sandbox-network", false, "with --sandbox, allow commands to use the network")
	fs.BoolVar(&o.plan, "plan", false, "enable the update_plan tool: a task plan saved in the checkpoint and pinned in every request (default off; config plan.enabled)")
	fs.BoolVar(&o.skills, "skills", false, "discover Agent Skills in .agents/skills (project, up to the repo root, and ~/.agents/skills) and add the skill tool (default off; config skills)")
	fs.BoolVar(&o.noSkills, "no-skills", false, "turn Agent Skills discovery off, overriding config skills; --no-skills=false turns it on")
	fs.StringVar(&o.planFile, "plan-file", "", "with the plan enabled, also export the current plan as Markdown to this file after each save")
	fs.StringVar(&o.reasoningEffort, "reasoning-effort", "", "provider reasoning effort: none, minimal, low, medium, high, xhigh, or max")
	fs.StringVar(&o.openaiAPI, "openai-api", "", "openai provider transport: auto, responses, or chat")
	fs.StringVar(&o.execEnv, "env", "", "where bash and start_process run: local or container (default: config's environment, else local)")
	fs.BoolVar(&o.jsonOutput, "json", false, "write run events as JSONL to stdout")
	fs.StringVar(&o.outputSchema, "output-schema", "", "end the run with a JSON result validated against this JSON Schema file (printed to stdout)")
	fs.Var(&o.logDB, "log-db", "also log run events to SQLite (default .tinyagent/tinyagent.db under --cwd); --log-db=false turns config log_db off")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&o.showVersion, "V", false, "shorthand for --version")
	fs.BoolVar(&o.doUpdate, "update", false, "update to the latest release and exit")
	return fs
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == tools.SandboxHelperArg {
		os.Exit(tools.RunSandboxHelper(os.Args[2:]))
	}
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
	if !config.ValidEnvironment(o.execEnv) {
		fmt.Fprintf(fs.Output(), "error: --env %q must be local or container\n", o.execEnv)
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
		if flagWasSet(fs, "output-schema") {
			fmt.Fprintln(os.Stderr, "error: --resume and --output-schema are mutually exclusive; runs with an output schema cannot be resumed")
			return 2
		}
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
	var schema *outschema.Schema
	if o.outputSchema != "" {
		if schema, err = outschema.Load(o.outputSchema); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
	}
	// With a schema, stdout carries only the validated result (or JSONL), so
	// progress text goes to stderr.
	progress := io.Writer(os.Stdout)
	if schema != nil {
		progress = os.Stderr
	}

	cfgPath := config.Find(o.configPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fail(err)
	}
	hookRunner, err := hooks.New(cfg.Hooks, cfgPath)
	if err != nil {
		return fail(err)
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "max-format-errors") {
		cfg.MaxFormatErrors = o.maxFormatErrors
	}
	if flagWasSet(fs, "read-only") {
		cfg.ReadOnly = o.readOnly
	}
	applySandboxFlags(fs, &cfg, o)
	if flagWasSet(fs, "plan") {
		cfg.Plan.Enabled = o.plan
	}
	if o.planFile != "" && !cfg.Plan.Enabled {
		return fail(errors.New("--plan-file requires the plan tool; pass --plan or set plan.enabled: true"))
	}
	if on, set, err := skillsFlag(fs, o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	} else if set {
		cfg.Skills = on
	}
	if msg := envConflict(cfg); msg != "" {
		fmt.Fprintln(os.Stderr, "error: "+msg)
		return 2
	}
	o.modelName = resolveModel(fs, o.modelName, cfg.Model)
	if flagWasSet(fs, "reasoning-effort") {
		cfg.ReasoningEffort = o.reasoningEffort
	}
	if err := checkReasoningEffort(o.modelName, cfg.ReasoningEffort); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if flagWasSet(fs, "openai-api") {
		cfg.OpenAIAPI = o.openaiAPI
	}
	if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	openaiAPI := model.OpenAITransport(o.modelName, cfg.OpenAIAPI)
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
	env, closeEnv, err := startEnvironment(ctx, cfg, root)
	if err != nil {
		return fail(err)
	}
	defer closeEnv()
	// MCP discovery counts against the wall-clock limit. Agent.Run applies
	// the same limit again, and the earlier deadline set here wins.
	if cfg.WallTimeSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.WallTimeSec)*time.Second)
		defer cancel()
	}
	mcpRegistry, err := mcptools.Start(ctx, root, mcpServers(os.Stderr, cfg), os.Stderr)
	if err != nil {
		return fail(err)
	}
	defer mcpRegistry.Close()

	var onText func(string)
	if !o.noStream && !o.jsonOutput {
		onText = func(s string) { io.WriteString(progress, s) }
	}
	if openaiAPI != "" && o.verbose {
		fmt.Fprintf(os.Stderr, "openai transport: %s\n", openaiAPI)
	}
	m, err := model.NewWith(o.modelName, onText, model.Options{OpenAIAPI: openaiAPI})
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, o.modelName, err.Error())
		return code
	}
	if err := model.SetReasoningEffort(m, o.modelName, cfg.ReasoningEffort); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	warnUnpriced(os.Stderr, o.modelName, cfg.CostLimit)

	a := &agent.Agent{
		Model:           m,
		ModelName:       o.modelName,
		ReasoningEffort: cfg.ReasoningEffort,
		OpenAIAPI:       openaiAPI,
		Tools: &tools.Executor{
			Root:           root,
			ReadOnly:       cfg.ReadOnly,
			Env:            env,
			BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars: cfg.MaxOutputChars,
			MaxProcesses:   cfg.MaxBackgroundProcesses,
			Stream:         streamTo(jsonStream(o.jsonOutput), progress),
			ExternalSpecs:  mcpRegistry.Specs,
			ExternalCall:   mcpRegistry.Call,
			Plan:           cfg.Plan.Enabled,
		},
		Config:       cfg,
		Hooks:        hookRunner,
		Out:          outputTo(runOutput(o.jsonOutput), progress),
		Err:          os.Stderr,
		Verbose:      o.verbose,
		Streamed:     !o.noStream,
		SkipAgentsMD: o.noAgentsMD,
		NoCompact:    o.noCompact,
		OutputSchema: schema,
	}
	var events *runEvents
	warn := func(msg string) { events.warn(msg) }
	db, dbErr := openLogDB(fs, o, cfg, root, task, o.modelName, false, a, warn)
	events = startEvents(os.Stdout, o.jsonOutput, a, false, stop, db)
	if events.err != nil {
		events.closeDB()
		return 1
	}
	defer events.closeDB()
	if dbErr != nil {
		warn(dbErr.Error())
	}
	if cfg.Skills {
		loadSkills(a, root, warn)
	}
	if o.output != "" {
		a.OnStep = func(t *trajectory.Trajectory) error {
			if err := t.Save(o.output); err != nil {
				fmt.Fprintln(os.Stderr, "warning: saving trajectory:", err)
				if a.OnEvent != nil {
					a.OnEvent("warning", map[string]any{"message": "saving trajectory: " + err.Error()})
				}
				return nil
			}
			checkpointSaved(a, t, o.output, o.planFile)
			return nil
		}
	} else if o.planFile != "" {
		a.OnStep = func(t *trajectory.Trajectory) error {
			exportPlan(a, t, o.planFile)
			return nil
		}
	}

	start := time.Now()
	t := a.Run(ctx, task)
	elapsed := time.Since(start)
	reportSummary(os.Stderr, o.modelName, t, elapsed)
	if events.finish(t, elapsed) != nil {
		return 1
	}
	if schema != nil {
		if t.ExitStatus != agent.StatusSubmitted || t.StructuredOutput == nil {
			return 1
		}
		if !o.jsonOutput {
			if _, err := os.Stdout.Write(append(append([]byte(nil), t.StructuredOutput...), '\n')); err != nil {
				return fail(err)
			}
		}
		return 0
	}
	if t.ExitStatus == agent.StatusSubmitted || t.ExitStatus == agent.StatusCompleted {
		return 0
	}
	return 1
}

// streamTo returns progress in place of os.Stdout for bash streaming; nil
// (JSON mode) stays nil.
func streamTo(w, progress io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return progress
}

// outputTo returns progress in place of os.Stdout for agent output;
// io.Discard (JSON mode) is kept.
func outputTo(w, progress io.Writer) io.Writer {
	if w == io.Discard {
		return w
	}
	return progress
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
	// Like the model, the recorded effort is kept for the whole run (it is
	// part of the provider's prompt cache key); the config value is ignored.
	if flagWasSet(fs, "reasoning-effort") && o.reasoningEffort != src.ReasoningEffort {
		return fail(fmt.Errorf("resume: --reasoning-effort %q conflicts with the checkpoint's reasoning_effort %q", o.reasoningEffort, src.ReasoningEffort))
	}
	if err := checkReasoningEffort(modelName, src.ReasoningEffort); err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}

	cfgPath := config.Find(o.configPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fail(err)
	}
	hookRunner, err := hooks.New(cfg.Hooks, cfgPath)
	if err != nil {
		return fail(err)
	}
	if src.MCPEnabled || len(src.ExternalTools) > 0 {
		return fail(errors.New("resume: MCP-enabled runs cannot be resumed; start a fresh task"))
	}
	if len(cfg.MCPServers) > 0 {
		fmt.Fprintln(os.Stderr, "resume: configured MCP servers are not started; resumed runs use only the built-in tools")
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "max-format-errors") {
		cfg.MaxFormatErrors = o.maxFormatErrors
	}
	if flagWasSet(fs, "read-only") && o.readOnly != src.ReadOnly {
		return fail(errors.New("resume: --read-only conflicts with the checkpoint's recorded mode; start a fresh task"))
	}
	cfg.ReadOnly = src.ReadOnly
	if flagWasSet(fs, "plan") && o.plan != src.PlanEnabled {
		return fail(errors.New("resume: --plan conflicts with the checkpoint's recorded plan setting; start a fresh task"))
	}
	cfg.Plan.Enabled = src.PlanEnabled
	if on, set, err := skillsFlag(fs, *o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	} else if set && on != src.SkillsEnabled {
		return fail(errors.New("resume: --skills/--no-skills conflicts with the checkpoint's recorded skills setting; start a fresh task"))
	}
	cfg.Skills = src.SkillsEnabled
	if o.planFile != "" && !cfg.Plan.Enabled {
		return fail(errors.New("--plan-file requires the plan tool, and this checkpoint was recorded without it"))
	}
	applySandboxFlags(fs, &cfg, *o)
	if msg := envConflict(cfg); msg != "" {
		fmt.Fprintln(os.Stderr, "error: "+msg)
		return 2
	}
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, closeEnv, err := startEnvironment(ctx, cfg, root)
	if err != nil {
		return fail(err)
	}
	defer closeEnv()

	var onText func(string)
	if !o.noStream && !o.jsonOutput {
		onText = func(s string) { os.Stdout.WriteString(s) }
	}
	// The recorded transport is kept: the checkpoint's reasoning items only
	// replay on the transport that produced them. Older checkpoints, with
	// none recorded, resolve like a new run.
	openaiAPI := src.OpenAIAPI
	if flagWasSet(fs, "openai-api") {
		if err := checkOpenAIAPI(o.openaiAPI); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		want := model.OpenAITransport(modelName, o.openaiAPI)
		if openaiAPI != "" && want != openaiAPI {
			return fail(fmt.Errorf("resume: --openai-api %q conflicts with the checkpoint's openai_api %q", o.openaiAPI, openaiAPI))
		}
		cfg.OpenAIAPI = o.openaiAPI
	}
	if openaiAPI == "" {
		if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		openaiAPI = model.OpenAITransport(modelName, cfg.OpenAIAPI)
	}
	if openaiAPI != "" && o.verbose {
		fmt.Fprintf(os.Stderr, "openai transport: %s\n", openaiAPI)
	}
	m, err := model.NewWith(modelName, onText, model.Options{OpenAIAPI: openaiAPI})
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, modelName, err.Error())
		return code
	}
	if err := model.SetReasoningEffort(m, modelName, src.ReasoningEffort); err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}
	warnUnpriced(os.Stderr, modelName, cfg.CostLimit)

	// The agent records this boundary and clears the active termination before
	// persisting the new starting state.
	prevStatus := src.ExitStatus
	if prevStatus == "" {
		prevStatus = "in progress"
	}

	a := &agent.Agent{
		Model:           m,
		ModelName:       modelName,
		ReasoningEffort: src.ReasoningEffort,
		OpenAIAPI:       openaiAPI,
		Tools: &tools.Executor{
			Root:           root,
			ReadOnly:       src.ReadOnly,
			Env:            env,
			BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars: cfg.MaxOutputChars,
			MaxProcesses:   cfg.MaxBackgroundProcesses,
			Stream:         jsonStream(o.jsonOutput),
			Plan:           src.PlanEnabled,
		},
		Config:       cfg,
		Hooks:        hookRunner,
		Out:          runOutput(o.jsonOutput),
		Err:          os.Stderr,
		Verbose:      o.verbose,
		Streamed:     !o.noStream,
		SkipAgentsMD: src.SkipAgentsMD,
		NoCompact:    o.noCompact,
		Resume:       src,
		ResumeSource: o.resume,
	}
	var events *runEvents
	warn := func(msg string) { events.warn(msg) }
	db, dbErr := openLogDB(fs, *o, cfg, root, src.Task, modelName, true, a, warn)
	events = startEvents(os.Stdout, o.jsonOutput, a, true, stop, db)
	if events.err != nil {
		events.closeDB()
		return 1
	}
	defer events.closeDB()
	if dbErr != nil {
		warn(dbErr.Error())
	}
	if src.SkillsEnabled {
		loadSkills(a, root, warn)
	}
	initial := true
	a.OnStep = func(t *trajectory.Trajectory) error {
		if initial {
			if err := t.SaveNew(o.output); err != nil {
				return err
			}
			initial = false
		} else if err := t.Save(o.output); err != nil {
			return err
		}
		checkpointSaved(a, t, o.output, o.planFile)
		return nil
	}

	fmt.Fprintf(os.Stderr, "resuming %s from %s (previous status %s after %d steps)\n", modelName, o.resume, prevStatus, src.Steps)
	fmt.Fprintln(os.Stderr, "note: inspect and revalidate the workspace; it may have changed and commands may have produced unrecorded side effects after the last save. Live processes are not restored")

	start := time.Now()
	t := a.Run(ctx, src.Task)
	elapsed := time.Since(start)
	reportSummary(os.Stderr, modelName, t, elapsed)
	if events.finish(t, elapsed) != nil {
		return 1
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

// applySandboxFlags lets --sandbox, --sandbox-network and --env override the
// config.
func applySandboxFlags(fs *flag.FlagSet, cfg *config.AgentConfig, o options) {
	if flagWasSet(fs, "env") {
		cfg.Environment = o.execEnv
	}
	if flagWasSet(fs, "sandbox") {
		cfg.Sandbox = o.sandbox
	}
	if flagWasSet(fs, "sandbox-network") {
		cfg.SandboxNetwork = o.sandboxNetwork
	}
}

// envConflict reports settings that can't be combined.
func envConflict(cfg config.AgentConfig) string {
	if cfg.Environment == "container" && cfg.Sandbox && !cfg.ReadOnly {
		return "--sandbox applies only to the local environment; it can't be combined with the container environment"
	}
	return ""
}

// startEnvironment returns the environment the config asks for and a
// function that releases it. It fails if the environment can't be set up
// (never falling back to an unconfined local one), and otherwise announces
// any sandbox or container on stderr. Read-only runs have no command tools,
// so they get a plain local environment.
func startEnvironment(ctx context.Context, cfg config.AgentConfig, root string) (tools.Environment, func(), error) {
	if cfg.ReadOnly {
		return tools.Local{}, func() {}, nil
	}
	if cfg.Environment == "container" {
		c, err := tools.StartContainer(ctx, cfg.Container.Runtime, cfg.Container.Image, cfg.Container.RunArgs, root)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintln(os.Stderr, c.Describe())
		return c, func() {
			if err := c.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "warning:", err)
			}
		}, nil
	}
	if !cfg.Sandbox {
		return tools.Local{}, func() {}, nil
	}
	sb, err := tools.NewSandbox(root, cfg.SandboxWritablePaths, cfg.SandboxNetwork)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintln(os.Stderr, sb.Describe())
	return tools.Local{Sandbox: sb}, sb.Cleanup, nil
}

// usageGroups lays out --help: each flag's shorthand, long name and value
// placeholder, by group. Help text and defaults come from the FlagSet.
var usageGroups = []struct {
	name  string
	flags [][3]string
}{
	{"", [][3]string{{"h", "help", ""}}},
	{"Task", [][3]string{{"t", "task", "string"}, {"m", "model", "string"}, {"", "reasoning-effort", "level"}, {"", "openai-api", "api"}, {"c", "config", "path"}, {"", "cwd", "dir"}, {"", "resume", "path"}}},
	{"Limits", [][3]string{{"", "step-limit", "n"}, {"", "cost-limit", "usd"}, {"", "time-limit", "sec"}, {"", "max-format-errors", "n"}, {"", "no-compact", ""}}},
	{"Output", [][3]string{{"o", "output", "path"}, {"v", "verbose", ""}, {"", "no-stream", ""}, {"", "json", ""}, {"", "output-schema", "path"}, {"", "log-db", "[=path]"}, {"", "no-agents-md", ""}, {"", "read-only", ""}, {"", "sandbox", ""}, {"", "sandbox-network", ""}, {"", "env", "name"}}},
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
				if _, err := strconv.Atoi(d); err == nil {
					help += fmt.Sprintf(" (default %s)", d)
				} else {
					help += fmt.Sprintf(" (default %q)", d)
				}
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
	if t.FormatErrors > 0 {
		s += fmt.Sprintf(", %d format errors", t.FormatErrors)
	}
	if t.ExitStatus == agent.StatusError {
		s += ": " + t.Submission
	}
	if t.ExitStatus == agent.StatusFormatError && t.LastFormatError != "" {
		s += ": " + t.LastFormatError
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
	case o.maxFormatErrors < 1:
		return "--max-format-errors must be a whole number >= 1"
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

// runEvents fans run events out to the enabled sinks: the --json JSONL
// stream and the SQLite log. Both receive the same events.
type runEvents struct {
	fan  eventlog.Fanout
	json *eventlog.JSONL
	db   *eventlog.SQLite
	err  error // first JSONL write error
}

func (e *runEvents) emit(kind string, fields map[string]any) {
	e.fan.Emit(kind, fields)
	if e.json != nil && e.err == nil {
		e.err = e.json.Err()
	}
}

// warn reports a log-db problem on stderr and, with --json, as a warning
// event. It is not sent to the database that failed.
func (e *runEvents) warn(msg string) {
	fmt.Fprintln(os.Stderr, "warning:", msg)
	if e != nil && e.json != nil {
		e.json.Emit("warning", map[string]any{"message": msg})
		if e.err == nil {
			e.err = e.json.Err()
		}
	}
}

func (e *runEvents) closeDB() {
	if e.db != nil {
		e.db.Close()
		e.db = nil
	}
}

// startEvents writes run.started (and the unpriced cost-limit warning) and
// routes a's events to the JSONL stream on out (when enabled) and to db
// (when non-nil). A failed JSONL write cancels the run through stop. With
// no sinks it returns a no-op stream and leaves a.OnEvent nil.
func startEvents(out io.Writer, enabled bool, a *agent.Agent, resumed bool, stop func(), db *eventlog.SQLite) *runEvents {
	events := &runEvents{}
	if enabled {
		events.json = eventlog.NewJSONL(out)
		events.fan = append(events.fan, events.json)
	}
	if db != nil {
		events.db = db
		events.fan = append(events.fan, db)
	}
	if len(events.fan) == 0 {
		return events
	}
	started := map[string]any{"schema_version": 1, "model": a.ModelName, "root": a.Tools.Root, "read_only": a.Tools.ReadOnly, "resumed": resumed}
	if a.ReasoningEffort != "" {
		started["reasoning_effort"] = a.ReasoningEffort
	}
	if a.OpenAIAPI != "" {
		started["openai_api"] = a.OpenAIAPI
	}
	events.emit("run.started", started)
	a.OnEvent = func(kind string, fields map[string]any) {
		events.emit(kind, fields)
		if events.err != nil {
			stop()
		}
	}
	if a.Config.CostLimit > 0 && model.Unpriced(a.ModelName) {
		events.emit("warning", map[string]any{"message": fmt.Sprintf("no pricing available for %s; --cost-limit cannot be enforced", a.ModelName)})
	}
	return events
}

// finish writes the closing events for t and returns the first JSONL write
// error. The database gets result first and is flushed and closed, so any
// log-db warning precedes the JSONL result, which stays the last line.
func (e *runEvents) finish(t *trajectory.Trajectory, elapsed time.Duration) error {
	if len(e.fan) == 0 {
		return nil
	}
	if t.ExitStatus == agent.StatusError {
		e.emit("error", map[string]any{"message": t.Submission})
	}
	result := resultFields(t, elapsed)
	if e.db != nil {
		e.db.Emit("result", result)
		e.closeDB()
	}
	if e.json != nil {
		e.json.Emit("result", result)
		if e.err == nil {
			e.err = e.json.Err()
		}
	}
	return e.err
}

// optionalPath is a flag that may be given bare (--log-db) or with a value
// (--log-db=path). --log-db=false turns it off.
type optionalPath struct {
	on   bool
	path string
}

func (p *optionalPath) String() string   { return "" }
func (p *optionalPath) IsBoolFlag() bool { return true }
func (p *optionalPath) Set(v string) error {
	switch v {
	case "true":
		p.on, p.path = true, ""
	case "false":
		p.on, p.path = false, ""
	case "":
		return errors.New("empty path")
	default:
		p.on, p.path = true, v
	}
	return nil
}

const defaultLogDB = ".tinyagent/tinyagent.db"

// logDBPath returns whether the SQLite log is on and its file. --log-db
// overrides config log_db.enabled; a --log-db path is relative to the shell,
// a config path relative to root (--cwd).
func logDBPath(fs *flag.FlagSet, o options, cfg config.LogDB, root string) (bool, string) {
	on, path := cfg.Enabled, cfg.Path
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	if flagWasSet(fs, "log-db") {
		on = o.logDB.on
		if o.logDB.path != "" {
			path, _ = filepath.Abs(o.logDB.path)
		}
	}
	if path == "" {
		path = filepath.Join(root, defaultLogDB)
	}
	return on, path
}

// openLogDB opens the SQLite event log when it is enabled. A non-nil error
// means the run goes ahead without it, after one warning.
func openLogDB(fs *flag.FlagSet, o options, cfg config.AgentConfig, root, task, modelName string, resumed bool, a *agent.Agent, warn func(string)) (*eventlog.SQLite, error) {
	on, path := logDBPath(fs, o, cfg.LogDB, root)
	if !on {
		return nil, nil
	}
	provider := "openai"
	if p, _, ok := strings.Cut(modelName, "/"); ok {
		provider = p
	}
	db, err := eventlog.Open(eventlog.Options{
		Path: path, Level: cfg.LogDB.Level,
		MaxDays: cfg.LogDB.MaxDays, MaxRuns: cfg.LogDB.MaxRuns, MaxPayloadBytes: cfg.LogDB.MaxPayloadBytes,
		Redactor: eventlog.EnvRedactor(configuredSecrets(cfg)...),
		Run:      eventlog.RunInfo{Cwd: root, Model: modelName, Provider: provider, Task: task, Version: currentVersion(), Resumed: resumed},
		Step:     a.Step,
		Warn:     warn,
	})
	if err != nil {
		return nil, fmt.Errorf("%v; continuing without the event log", err)
	}
	return db, nil
}

// configuredSecrets returns the env values set for MCP servers and hooks in
// the config, which the event log redacts.
func configuredSecrets(cfg config.AgentConfig) []string {
	var envs []string
	for _, s := range cfg.MCPServers {
		envs = append(envs, s.Env...)
	}
	for _, h := range append(append([]config.Hook(nil), cfg.Hooks.PreTool...), cfg.Hooks.PostTool...) {
		envs = append(envs, h.Env...)
	}
	var vals []string
	for _, kv := range envs {
		if _, v, ok := strings.Cut(kv, "="); ok {
			vals = append(vals, v)
		}
	}
	return vals
}

// mcpServers returns the MCP servers to start. Read-only runs start none:
// a server is an arbitrary local executable, and its tools are never offered
// there anyway.
func mcpServers(w io.Writer, cfg config.AgentConfig) []config.MCPServer {
	if cfg.ReadOnly && len(cfg.MCPServers) > 0 {
		fmt.Fprintln(w, "read-only mode: configured MCP servers are not started")
		return nil
	}
	return cfg.MCPServers
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
	fields := map[string]any{
		"status": t.ExitStatus, "submission": t.Submission, "steps": t.Steps,
		"input_tokens": t.InputTokens, "output_tokens": t.OutputTokens,
		"cache_read_tokens": t.CacheReadTokens, "cache_write_tokens": t.CacheWriteTokens,
		"cost_usd": t.Cost, "duration_ms": elapsed.Milliseconds(),
		"format_errors": t.FormatErrors,
	}
	if t.ExitStatus == agent.StatusSubmitted && t.StructuredOutput != nil {
		fields["structured_output"] = t.StructuredOutput
	}
	return fields
}

// checkpointSaved reports a completed save and then exports the plan mirror,
// which is always written after the checkpoint that contains it.
func checkpointSaved(a *agent.Agent, t *trajectory.Trajectory, path, planFile string) {
	if a.OnEvent != nil {
		a.OnEvent("checkpoint.saved", map[string]any{"step": t.Steps, "path": path, "pending": len(t.PendingCalls)})
	}
	exportPlan(a, t, planFile)
}

// exportPlan writes the read-only Markdown mirror of the plan. Failures only
// warn: the checkpoint is the source of truth.
func exportPlan(a *agent.Agent, t *trajectory.Trajectory, planFile string) {
	if planFile == "" {
		return
	}
	if err := writePlanFile(planFile, t.Plan); err != nil {
		fmt.Fprintln(os.Stderr, "warning: writing plan file:", err)
		if a.OnEvent != nil {
			a.OnEvent("warning", map[string]any{"message": "writing plan file: " + err.Error()})
		}
	}
}

func writePlanFile(path string, p *trajectory.Plan) error {
	var sb strings.Builder
	sb.WriteString("# Plan\n\n")
	if p == nil {
		sb.WriteString("_No plan yet._\n")
	} else {
		fmt.Fprintf(&sb, "Version %d, updated at step %d.\n\n", p.Version, p.UpdatedStep)
		for _, it := range p.Items {
			box := map[string]string{"pending": "[ ]", "in_progress": "[ ] (in progress)", "done": "[x]", "blocked": "[ ] (blocked)"}[it.Status]
			fmt.Fprintf(&sb, "- %s %s: %s\n", box, it.ID, it.Text)
		}
		if p.Note != "" {
			fmt.Fprintf(&sb, "\n%s\n", p.Note)
		}
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tinyagent-plan-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(sb.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// checkOpenAIAPI rejects an unknown openai_api value.
func checkOpenAIAPI(v string) error {
	if !model.ValidOpenAIAPI(v) {
		return fmt.Errorf("--openai-api/openai_api %q is invalid (want one of %s)", v, strings.Join(model.OpenAIAPIs, ", "))
	}
	return nil
}

// checkReasoningEffort rejects an unknown effort, or one the provider of spec
// has no control for, before any provider request.
func checkReasoningEffort(spec, effort string) error {
	if effort == "" {
		return nil
	}
	if !model.ValidReasoningEffort(effort) {
		return fmt.Errorf("--reasoning-effort/reasoning_effort %q is invalid (want one of %s)", effort, strings.Join(model.ReasoningEfforts, ", "))
	}
	provider, _, ok := strings.Cut(spec, "/")
	if !ok {
		provider = "openai"
	}
	switch provider {
	case "openai", "openrouter", "anthropic", "codex":
		return nil
	}
	return fmt.Errorf("--reasoning-effort/reasoning_effort is not supported by the %s provider", provider)
}

// skillsFlag returns the skills setting from --skills and --no-skills, and
// whether either was set. Contradictory values are an error.
func skillsFlag(fs *flag.FlagSet, o options) (on, set bool, err error) {
	a, b := flagWasSet(fs, "skills"), flagWasSet(fs, "no-skills")
	switch {
	case a && b && o.skills == o.noSkills:
		return false, false, errors.New("--skills and --no-skills conflict")
	case a:
		return o.skills, true, nil
	case b:
		return !o.noSkills, true, nil
	}
	return false, false, nil
}

// loadSkills discovers skills for a run rooted at root, reporting each
// problem through warn and, with -v, one line per skill.
func loadSkills(a *agent.Agent, root string, warn func(string)) {
	set, warnings := skills.Discover(root, skills.UserDir())
	for _, w := range warnings {
		warn(w)
	}
	a.SkillsEnabled = true
	a.Tools.Skills = set
	if a.Verbose {
		for _, k := range set.Skills {
			fmt.Fprintf(a.Out, "loaded skill %s (%s) from %s\n", k.Name, k.Scope, k.Path)
		}
	}
}
