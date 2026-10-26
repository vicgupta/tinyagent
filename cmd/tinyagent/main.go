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
	"github.com/vicgupta/tinyagent/internal/lsp"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/sandboxnet"
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
	images                                           bool
	applyPatch                                       bool
	subagents                                        bool
	webFetch                                         bool
	webFetchAllowDomains                             domainList
	lsp                                              bool
	scopedAgentsMD                                   bool
	toolOutputFiles                                  bool
	skills, noSkills                                 bool
	reasoningEffort                                  string
	fallbackModels                                   specList
	noFallback                                       bool
	openaiAPI                                        string
	execEnv                                          string
	commandEnv                                       string
	stepLimit, timeLimit, maxFormatErrors            int
	maxOutputTokens                                  int
	costLimit                                        float64
	verbose, noStream, noAgentsMD, noCompact         bool
	readOnly, sandbox, sandboxNetwork                bool
	sandboxAllowDomains                              domainList
	addDirRead, addDirWrite                          dirList
	jsonOutput                                       bool
	logDB                                            optionalPath
	worktree                                         worktreeFlag
	snapshots, dryRun                                bool
	rewind, rewindTo                                 string
	showHelp, showVersion, doUpdate, check           bool
}

// newFlagSet defines the command-line flags, storing their values in o. Each
// long name also works with a single dash (-task), as with any Go flag.
func newFlagSet(o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("tinyagent", flag.ContinueOnError)
	fs.SetOutput(flagOutput)
	fs.Usage = func() { printUsage(fs.Output(), fs) }
	fs.BoolVar(&o.showHelp, "help", false, "show this help and exit")
	fs.BoolVar(&o.showHelp, "h", false, "shorthand for --help")
	fs.StringVar(&o.task, "task", "", `task/prompt (required unless --resume; with --resume of a finished run, the follow-up instruction); "-" reads it from stdin`)
	fs.StringVar(&o.task, "t", "", "shorthand for --task")
	fs.StringVar(&o.modelName, "model", defaultModel(), "provider/model; if unset, $AGENT_MODEL, then the config's model")
	fs.StringVar(&o.modelName, "m", defaultModel(), "shorthand for --model")
	fs.StringVar(&o.configPath, "config", "", "YAML config (default: user config if present, else built-in)")
	fs.StringVar(&o.configPath, "c", "", "shorthand for --config")
	fs.StringVar(&o.cwd, "cwd", ".", "working directory")
	fs.Var(&o.worktree, "worktree", "run in a new Git worktree on branch tinyagent/<name> from HEAD of the repo containing --cwd, kept afterwards (default name generated)")
	fs.IntVar(&o.stepLimit, "step-limit", 0, "max model calls, 0 = unlimited")
	fs.Float64Var(&o.costLimit, "cost-limit", 0, "max USD, 0 = unlimited")
	fs.IntVar(&o.timeLimit, "time-limit", 0, "max wall-clock seconds, 0 = unlimited")
	fs.IntVar(&o.maxFormatErrors, "max-format-errors", config.DefaultMaxFormatErrors, "stop after this many consecutive steps of malformed tool calls (>= 1)")
	fs.IntVar(&o.maxOutputTokens, "max-output-tokens", 0, "per-request output-token budget sent to the provider, 0 = provider default (8192 for anthropic; config max_output_tokens)")
	fs.StringVar(&o.output, "output", "", "write trajectory JSON")
	fs.StringVar(&o.output, "o", "", "shorthand for --output")
	fs.StringVar(&o.resume, "resume", "", "continue an unfinished run from a trajectory JSON checkpoint, or a finished one with -t \"<follow-up>\"")
	fs.BoolVar(&o.verbose, "verbose", false, "print tool outputs")
	fs.BoolVar(&o.verbose, "v", false, "shorthand for --verbose")
	fs.BoolVar(&o.noStream, "no-stream", false, "wait for full responses instead of streaming")
	fs.BoolVar(&o.noAgentsMD, "no-agents-md", false, "don't add AGENTS.md (nearest, up to the repo root) to the system prompt")
	fs.BoolVar(&o.scopedAgentsMD, "scoped-agents-md", false, "also deliver AGENTS.md files in directories below --cwd, appended to the first file tool result that touches a path there (default off; config scoped_agents_md)")
	fs.BoolVar(&o.noCompact, "no-compact", false, "don't compact the history when it nears the context window")
	fs.BoolVar(&o.readOnly, "read-only", false, "allow only read_file, list_files, and grep; blocks edits and commands")
	fs.BoolVar(&o.sandbox, "sandbox", false, "confine bash and start_process commands: writes only under --cwd, a temp dir and sandbox_writable_paths")
	fs.BoolVar(&o.sandboxNetwork, "sandbox-network", false, "with --sandbox, allow commands to use the network")
	fs.Var(&o.sandboxAllowDomains, "sandbox-allow-domain", "with --sandbox, let commands reach only this host (name, *.domain or IP, optional :port) over HTTP(S) via a filtering proxy; repeatable, replaces sandbox_allowed_domains")
	fs.Var(&o.addDirRead, "add-dir-read", "let the file tools (and a container) read this directory outside --cwd; repeatable; with --add-dir-write, replaces config additional_dirs")
	fs.Var(&o.addDirWrite, "add-dir-write", "let the file tools, --sandbox commands and a container read and write this directory outside --cwd; repeatable; with --add-dir-read, replaces config additional_dirs")
	fs.BoolVar(&o.plan, "plan", false, "enable the update_plan tool: a task plan saved in the checkpoint and pinned in every request (default off; config plan.enabled)")
	fs.BoolVar(&o.toolOutputFiles, "tool-output-files", false, "save the full text of over-limit tool results under .tinyagent/tool-output so the model can page it with read_file and grep (default off; config tool_output_files.enabled)")
	fs.BoolVar(&o.applyPatch, "apply-patch", false, "add the apply_patch tool: multi-file, multi-hunk edits in the *** Begin Patch format, checked before any write (default off; config apply_patch)")
	fs.BoolVar(&o.webFetch, "web-fetch", false, "add the web_fetch tool: read an allowlisted http(s) URL as text, in tinyagent's own process, separate from command network access (default off; config web_fetch.enabled)")
	fs.Var(&o.webFetchAllowDomains, "web-fetch-allow-domain", "with --web-fetch, let web_fetch read this host (name, *.domain or IP, optional :port); repeatable, replaces web_fetch.allowed_domains")
	fs.BoolVar(&o.lsp, "lsp", false, "start the configured language servers (lsp.servers in the user config or -c): append compiler diagnostics to edit results and add the read-only lsp tool (default off; config lsp.enabled)")
	fs.BoolVar(&o.subagents, "subagents", false, "add the delegate tool: hand a task to a read-only subagent with a fresh context and get back only its report (default off; config subagents.enabled)")
	fs.BoolVar(&o.images, "images", false, "let vision-capable models see images: the view_image tool and MCP image pass-through (default off; config images.enabled)")
	fs.BoolVar(&o.skills, "skills", false, "discover Agent Skills in .agents/skills (project, up to the repo root, and ~/.agents/skills) and add the skill tool (default off; config skills)")
	fs.BoolVar(&o.noSkills, "no-skills", false, "turn Agent Skills discovery off, overriding config skills; --no-skills=false turns it on")
	fs.StringVar(&o.planFile, "plan-file", "", "with the plan enabled, also export the current plan as Markdown to this file after each save")
	fs.StringVar(&o.reasoningEffort, "reasoning-effort", "", "provider reasoning effort: none, minimal, low, medium, high, xhigh, or max")
	fs.Var(&o.fallbackModels, "fallback-model", "provider/model to switch to when requests still fail with 429/5xx/network errors after retries (repeatable, tried in order; replaces fallback_models)")
	fs.BoolVar(&o.noFallback, "no-fallback", false, "don't fall back to other models (clears fallback_models)")
	fs.StringVar(&o.openaiAPI, "openai-api", "", "openai provider transport: auto, responses, or chat")
	fs.StringVar(&o.execEnv, "env", "", "where bash and start_process run: local or container (default: config's environment, else local)")
	fs.StringVar(&o.commandEnv, "command-env", "", "environment bash and start_process inherit: all, core or none (default: config's command_env.inherit, else all)")
	fs.BoolVar(&o.jsonOutput, "json", false, "write run events as JSONL to stdout")
	fs.StringVar(&o.outputSchema, "output-schema", "", "end the run with a JSON result validated against this JSON Schema file (printed to stdout)")
	fs.BoolVar(&o.snapshots, "snapshots", false, "snapshot the workspace before every tool batch that can change files, for --rewind; requires -o (default off; config snapshots)")
	fs.StringVar(&o.rewind, "rewind", "", "restore --cwd to a workspace snapshot recorded in this trajectory JSON (see --to and --dry-run)")
	fs.StringVar(&o.rewindTo, "to", "", "with --rewind: start (the baseline, the default), a step number (the state before that step's tool batch), or a snapshot id")
	fs.BoolVar(&o.dryRun, "dry-run", false, "with --rewind: print what would change and write nothing")
	fs.Var(&o.logDB, "log-db", "also log run events to SQLite (default .tinyagent/tinyagent.db under --cwd); --log-db=false turns config log_db off")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.BoolVar(&o.showVersion, "V", false, "shorthand for --version")
	fs.BoolVar(&o.doUpdate, "update", false, "update to the latest release and exit")
	fs.BoolVar(&o.check, "check", false, "print the resolved config, model, provider key status, fallbacks, cwd and limits, then exit; makes no model request")
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
	if flagWasSet(fs, "command-env") && (o.commandEnv == "" || !config.ValidInherit(o.commandEnv)) {
		fmt.Fprintf(fs.Output(), "error: --command-env %q must be all, core or none\n", o.commandEnv)
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
	if o.check {
		return runCheck(&o, fs, os.Stdout)
	}
	if o.worktree.on && (o.resume != "" || o.rewind != "") {
		fmt.Fprintln(os.Stderr, "error: --worktree can't be combined with --resume or --rewind; pass the worktree path as --cwd instead")
		return 2
	}
	if o.rewind != "" {
		if o.resume != "" {
			fmt.Fprintln(os.Stderr, "error: --rewind and --resume are mutually exclusive")
			return 2
		}
		return runRewind(&o, fs)
	}
	if flagWasSet(fs, "to", "dry-run") {
		fmt.Fprintln(os.Stderr, "error: --to and --dry-run apply only to --rewind")
		return 2
	}
	if o.noAgentsMD && o.scopedAgentsMD {
		fmt.Fprintln(os.Stderr, "error: --no-agents-md and --scoped-agents-md can't be combined: --no-agents-md turns off all AGENTS.md handling, scoped files included; drop one of them")
		return 2
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
		return loadConfigError(err)
	}
	registerProviders(cfg)
	hookRunner, err := hooks.New(cfg.Hooks, cfgPath)
	if err != nil {
		return fail(err)
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "max-format-errors") {
		cfg.MaxFormatErrors = o.maxFormatErrors
	}
	if flagWasSet(fs, "max-output-tokens") {
		cfg.MaxOutputTokens = o.maxOutputTokens
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
	if flagWasSet(fs, "images") {
		cfg.Images.Enabled = o.images
	}
	if flagWasSet(fs, "apply-patch") {
		cfg.ApplyPatch = o.applyPatch
	}
	if flagWasSet(fs, "subagents") {
		cfg.Subagents.Enabled = o.subagents
	}
	if flagWasSet(fs, "web-fetch") {
		cfg.WebFetch.Enabled = o.webFetch
	}
	webFetch, err := newWebFetcher(fs, o, &cfg)
	if err != nil {
		return configFail(err)
	}
	if flagWasSet(fs, "lsp") {
		cfg.LSP.Enabled = o.lsp
	}
	if flagWasSet(fs, "scoped-agents-md") {
		cfg.ScopedAgentsMD = o.scopedAgentsMD
	}
	if code := checkLSP(cfg, cfgPath, ""); code != 0 {
		return code
	}
	if on, set, err := skillsFlag(fs, o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	} else if set {
		cfg.Skills = on
	}
	if flagWasSet(fs, "snapshots") {
		cfg.Snapshots.Enabled = o.snapshots
	}
	if code := checkSnapshots(&cfg, o.output); code != 0 {
		return code
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
	if err := checkMaxOutputTokens(o.modelName, cfg.MaxOutputTokens); err != nil {
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
	if err := model.CheckProviderEnv(o.modelName); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	fallbacks, err := fallbackModels(fs, o.fallbackModels, o.noFallback, cfg)
	if err == nil {
		err = checkFallbacks(o.modelName, fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	subagents, err := newSubagents(cfg, o.modelName, cfg.ReasoningEffort)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
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
	var worktree *trajectory.WorktreeInfo
	if o.worktree.on {
		if root, worktree, err = setupWorktree(root, o.worktree.name, os.Stderr, o.verbose); err != nil {
			if errors.Is(err, errInterruptedSetup) {
				return fail(err)
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
	}
	outputFiles, err := toolOutputFiles(fs, o, cfg, root)
	if err != nil {
		return fail(err)
	}
	fileAccess, err := newFileAccess(cfg, root, outputFiles, o.verbose)
	if err != nil {
		return configFail(err)
	}
	extraDirs, err := additionalDirs(fs, o, cfg, root, o.verbose)
	if err != nil {
		return configFail(err)
	}
	fileAccess.AddDirs(extraDirs)
	if cfg.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}
	lspTools := newLSP(cfg, root)
	if lspTools != nil {
		defer lspTools.Manager.Close()
	}
	warnSnapshotDirs(cfg, extraDirs)
	snaps, err := openSnapshots(fs, o, cfg, root, o.output)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, closeEnv, err := startEnvironment(ctx, cfg, root, extraDirs, o.verbose)
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
	announceProvider(os.Stderr, o.modelName, o.verbose)
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
	if err := model.SetMaxOutputTokens(m, o.modelName, cfg.MaxOutputTokens); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	warnUnpriced(os.Stderr, o.modelName, cfg.CostLimit)
	if m, err = withFallbacks(m, o.modelName, fallbacks, onText, cfg, cfg.ReasoningEffort, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	providerAPI, providerBaseURL := providerInfo(o.modelName)

	a := &agent.Agent{
		Model:           m,
		ModelName:       o.modelName,
		FallbackModels:  fallbacks,
		ReasoningEffort: cfg.ReasoningEffort,
		OpenAIAPI:       openaiAPI,
		ProviderAPI:     providerAPI,
		ProviderBaseURL: providerBaseURL,
		Tools: &tools.Executor{
			Root:               root,
			ReadOnly:           cfg.ReadOnly,
			Env:                env,
			BashTimeout:        time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars:     cfg.MaxOutputChars,
			MaxProcesses:       cfg.MaxBackgroundProcesses,
			Stream:             streamTo(jsonStream(o.jsonOutput), progress),
			ExternalSpecs:      mcpRegistry.Specs,
			ExternalCall:       mcpRegistry.Call,
			Plan:               cfg.Plan.Enabled,
			Images:             cfg.Images.Enabled,
			ApplyPatch:         cfg.ApplyPatch,
			Delegate:           subagents != nil,
			WebFetch:           webFetch,
			LSP:                lspTools,
			OutputFiles:        outputFiles,
			FileAccess:         fileAccess,
			ExtraDirs:          extraDirs,
			MaxImageBytes:      cfg.MaxImageBytes,
			ExternalCallImages: mcpRegistry.CallImages(cfg.MaxImageBytes),
		},
		Config:         cfg,
		Hooks:          hookRunner,
		Out:            outputTo(runOutput(o.jsonOutput), progress),
		Err:            os.Stderr,
		Verbose:        o.verbose,
		Streamed:       !o.noStream,
		SkipAgentsMD:   o.noAgentsMD,
		ScopedAgentsMD: cfg.ScopedAgentsMD && !o.noAgentsMD,
		NoCompact:      o.noCompact,
		OutputSchema:   schema,
		Snapshots:      snaps,
		Subagents:      subagents,
		Worktree:       worktree,
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
	startOutputFiles(outputFiles, cfg.ToolOutputFiles.MaxRuns, warn)
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
	if worktree != nil {
		fmt.Fprintf(os.Stderr, "worktree kept: %s (branch %s)\n", worktree.Path, worktree.Branch)
	}
	if events.finish(t, elapsed) != nil {
		return 1
	}
	if schema != nil {
		if t.ExitStatus == agent.StatusOutputTruncated {
			return exitOutputTruncated
		}
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
	if t.ExitStatus == agent.StatusOutputTruncated {
		return exitOutputTruncated
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
	taskSet := flagWasSet(fs, "task", "t")
	if !flagWasSet(fs, "cwd") {
		return fail(errors.New("resume: an explicit --cwd matching the checkpoint root is required"))
	}

	src, err := trajectory.Load(o.resume)
	if err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}
	// A finished checkpoint continues only with a follow-up task; an
	// unfinished one only without.
	finished := trajectory.Finished(src.ExitStatus)
	followUp := ""
	switch {
	case taskSet && !finished:
		fmt.Fprintln(os.Stderr, "error: --resume and --task are mutually exclusive for an unfinished checkpoint; --task adds a follow-up only to a finished (Submitted or Completed) run")
		return 2
	case finished && o.task == "":
		fmt.Fprintf(os.Stderr, "error: checkpoint already finished (%s); a follow-up task is required to continue it: --resume %s -t \"<follow-up>\"\n", src.ExitStatus, o.resume)
		return 2
	case finished:
		if followUp, err = readTask(o.task, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "error: follow-up task:", err)
			return 2
		}
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

	cfgPath := config.Find(o.configPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return loadConfigError(err)
	}
	registerProviders(cfg)
	// Checked after the config load so configured providers resolve.
	if err := checkReasoningEffort(modelName, src.ReasoningEffort); err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}
	if err := checkResumeProvider(src, modelName); err != nil {
		return fail(err)
	}
	// The fallback chain is this invocation's (config or flags), not the
	// checkpoint's; a resumed run starts on the primary model again.
	fallbacks, err := fallbackModels(fs, o.fallbackModels, o.noFallback, cfg)
	if err == nil {
		err = checkFallbacks(modelName, fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2
	}
	if err := model.CheckProviderEnv(modelName); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2
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
	// The output budget may change on resume, e.g. raised after OutputTruncated.
	if flagWasSet(fs, "max-output-tokens") {
		cfg.MaxOutputTokens = o.maxOutputTokens
	}
	if err := checkMaxOutputTokens(modelName, cfg.MaxOutputTokens); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2
	}
	if flagWasSet(fs, "read-only") && o.readOnly != src.ReadOnly {
		return fail(errors.New("resume: --read-only conflicts with the checkpoint's recorded mode; start a fresh task"))
	}
	cfg.ReadOnly = src.ReadOnly
	if flagWasSet(fs, "plan") && o.plan != src.PlanEnabled {
		return fail(errors.New("resume: --plan conflicts with the checkpoint's recorded plan setting; start a fresh task"))
	}
	cfg.Plan.Enabled = src.PlanEnabled
	if flagWasSet(fs, "images") && o.images != src.ImagesEnabled {
		return fail(errors.New("resume: --images conflicts with the checkpoint's recorded images setting; start a fresh task"))
	}
	cfg.Images.Enabled = src.ImagesEnabled
	if flagWasSet(fs, "apply-patch") && o.applyPatch != src.ApplyPatchEnabled {
		fmt.Fprintln(os.Stderr, "error: resume: --apply-patch conflicts with the checkpoint's recorded apply_patch setting; start a fresh task")
		return 2
	}
	cfg.ApplyPatch = src.ApplyPatchEnabled
	if flagWasSet(fs, "subagents") && o.subagents != src.SubagentsEnabled {
		fmt.Fprintln(os.Stderr, "error: resume: --subagents conflicts with the checkpoint's recorded subagents setting; start a fresh task")
		return 2
	}
	cfg.Subagents.Enabled = src.SubagentsEnabled
	if flagWasSet(fs, "web-fetch") && o.webFetch != src.WebFetchEnabled {
		fmt.Fprintln(os.Stderr, "error: resume: --web-fetch conflicts with the checkpoint's recorded web_fetch setting; start a fresh task")
		return 2
	}
	cfg.WebFetch.Enabled = src.WebFetchEnabled
	webFetch, err := newWebFetcher(fs, *o, &cfg)
	if err != nil {
		return configFail(err)
	}
	if flagWasSet(fs, "lsp") && o.lsp != src.LSPEnabled {
		fmt.Fprintln(os.Stderr, "error: resume: --lsp conflicts with the checkpoint's recorded lsp setting; start a fresh task")
		return 2
	}
	cfg.LSP.Enabled = src.LSPEnabled
	if flagWasSet(fs, "scoped-agents-md") && o.scopedAgentsMD != src.ScopedAgentsMDEnabled {
		fmt.Fprintln(os.Stderr, "error: resume: --scoped-agents-md conflicts with the checkpoint's recorded scoped_agents_md setting; start a fresh task")
		return 2
	}
	cfg.ScopedAgentsMD = src.ScopedAgentsMDEnabled
	if on, set, err := skillsFlag(fs, *o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	} else if set && on != src.SkillsEnabled {
		return fail(errors.New("resume: --skills/--no-skills conflicts with the checkpoint's recorded skills setting; start a fresh task"))
	}
	cfg.Skills = src.SkillsEnabled
	// Snapshots are per invocation: config or --snapshots decides for this
	// resume, and earlier snapshots stay in the trajectory either way.
	if flagWasSet(fs, "snapshots") {
		cfg.Snapshots.Enabled = o.snapshots
	}
	if cfg.ReadOnly && cfg.Snapshots.Enabled {
		fmt.Fprintln(os.Stderr, "read-only mode: workspace snapshots are not taken")
		cfg.Snapshots.Enabled = false
	}
	if o.planFile != "" && !cfg.Plan.Enabled {
		return fail(errors.New("--plan-file requires the plan tool, and this checkpoint was recorded without it"))
	}
	applySandboxFlags(fs, &cfg, *o)
	if msg := envConflict(cfg); msg != "" {
		fmt.Fprintln(os.Stderr, "error: "+msg)
		return 2
	}
	if code := checkLSP(cfg, cfgPath, "resume: "); code != 0 {
		return code
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
	outputFiles, err := toolOutputFiles(fs, *o, cfg, root)
	if err != nil {
		return fail(err)
	}
	fileAccess, err := newFileAccess(cfg, root, outputFiles, o.verbose)
	if err != nil {
		return configFail(err)
	}
	extraDirs, err := additionalDirs(fs, *o, cfg, root, o.verbose)
	if err != nil {
		return configFail(err)
	}
	fileAccess.AddDirs(extraDirs)
	if cur := agent.AdditionalDirsInfo(extraDirs, cfg.ReadOnly); len(cur)+len(src.AdditionalDirs) > 0 && fmt.Sprint(cur) != fmt.Sprint(src.AdditionalDirs) {
		fmt.Fprintln(os.Stderr, "resume: additional directories differ from the checkpoint's; this run uses the current ones")
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
	warnSnapshotDirs(cfg, extraDirs)
	lspTools := newLSP(cfg, root)
	if lspTools != nil {
		defer lspTools.Manager.Close()
	}
	snaps, err := openSnapshots(fs, *o, cfg, root, o.output, o.resume)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, closeEnv, err := startEnvironment(ctx, cfg, root, extraDirs, o.verbose)
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
	announceProvider(os.Stderr, modelName, o.verbose)
	m, err := model.NewWith(modelName, onText, model.Options{OpenAIAPI: openaiAPI})
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, modelName, err.Error())
		return code
	}
	if err := model.SetReasoningEffort(m, modelName, src.ReasoningEffort); err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}
	subagents, err := newSubagents(cfg, modelName, src.ReasoningEffort)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2
	}
	if err := model.SetMaxOutputTokens(m, modelName, cfg.MaxOutputTokens); err != nil {
		return fail(fmt.Errorf("resume: %w", err))
	}
	warnUnpriced(os.Stderr, modelName, cfg.CostLimit)
	if m, err = withFallbacks(m, modelName, fallbacks, onText, cfg, src.ReasoningEffort, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2
	}
	providerAPI, providerBaseURL := providerInfo(modelName)

	// The agent records this boundary and clears the active termination before
	// persisting the new starting state.
	prevStatus := src.ExitStatus
	if prevStatus == "" {
		prevStatus = "in progress"
	}

	a := &agent.Agent{
		Model:           m,
		ModelName:       modelName,
		FallbackModels:  fallbacks,
		ReasoningEffort: src.ReasoningEffort,
		OpenAIAPI:       openaiAPI,
		ProviderAPI:     providerAPI,
		ProviderBaseURL: providerBaseURL,
		Tools: &tools.Executor{
			Root:           root,
			ReadOnly:       src.ReadOnly,
			Env:            env,
			BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars: cfg.MaxOutputChars,
			MaxProcesses:   cfg.MaxBackgroundProcesses,
			Stream:         jsonStream(o.jsonOutput),
			Plan:           src.PlanEnabled,
			Images:         src.ImagesEnabled,
			ApplyPatch:     src.ApplyPatchEnabled,
			Delegate:       src.SubagentsEnabled,
			WebFetch:       webFetch,
			LSP:            lspTools,
			OutputFiles:    outputFiles,
			FileAccess:     fileAccess,
			ExtraDirs:      extraDirs,
			MaxImageBytes:  cfg.MaxImageBytes,
		},
		Config:         cfg,
		Hooks:          hookRunner,
		Out:            runOutput(o.jsonOutput),
		Err:            os.Stderr,
		Verbose:        o.verbose,
		Streamed:       !o.noStream,
		SkipAgentsMD:   src.SkipAgentsMD,
		ScopedAgentsMD: src.ScopedAgentsMDEnabled,
		NoCompact:      o.noCompact,
		Resume:         src,
		ResumeSource:   o.resume,
		Worktree:       src.Worktree,
		Snapshots:      snaps,
		FollowUp:       followUp,
		Subagents:      subagents,
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
	startOutputFiles(outputFiles, cfg.ToolOutputFiles.MaxRuns, warn)
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

	if finished {
		fmt.Fprintf(os.Stderr, "following up %s from %s (previous status %s after %d steps)\n", modelName, o.resume, prevStatus, src.Steps)
		fmt.Fprintln(os.Stderr, "note: the full previous conversation is sent to the provider again; the workspace may have changed since the run finished. Live processes are not restored")
	} else {
		fmt.Fprintf(os.Stderr, "resuming %s from %s (previous status %s after %d steps)\n", modelName, o.resume, prevStatus, src.Steps)
		fmt.Fprintln(os.Stderr, "note: inspect and revalidate the workspace; it may have changed and commands may have produced unrecorded side effects after the last save. Live processes are not restored")
	}

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
	if t.ExitStatus == agent.StatusOutputTruncated {
		return exitOutputTruncated
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

// applySandboxFlags lets --sandbox, --sandbox-network,
// --sandbox-allow-domain, --env and --command-env override the config.
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
	if flagWasSet(fs, "command-env") {
		cfg.CommandEnv.Inherit = o.commandEnv
	}
	if flagWasSet(fs, "sandbox-allow-domain") {
		cfg.SandboxAllowedDomains = o.sandboxAllowDomains
	}
}

// configFail reports a config error: exit status 2 for invalid settings
// (config.InvalidError), 1 otherwise, as before.
func configFail(err error) int {
	var invalid *config.InvalidError
	if errors.As(err, &invalid) {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return fail(err)
}

// envConflict reports settings that can't be combined.
func envConflict(cfg config.AgentConfig) string {
	if cfg.Environment == "container" && cfg.Sandbox && !cfg.ReadOnly {
		return "--sandbox applies only to the local environment; it can't be combined with the container environment"
	}
	if len(cfg.SandboxAllowedDomains) > 0 {
		if !cfg.Sandbox {
			return "sandbox_allowed_domains (--sandbox-allow-domain) requires --sandbox (sandbox: true)"
		}
		if cfg.SandboxNetwork {
			return "sandbox_allowed_domains (--sandbox-allow-domain) can't be combined with --sandbox-network (sandbox_network: true), which allows every host"
		}
	}
	return ""
}

// checkLSP reports language-server settings that can't start: enabled
// without servers, a forwarded variable that is unset, or the sandbox or
// container environment, which the servers (host processes) would escape.
// It returns the exit code (0 when fine).
func checkLSP(cfg config.AgentConfig, cfgPath, prefix string) int {
	if !cfg.LSP.Enabled {
		return 0
	}
	err := cfg.LSP.CheckEnabled(cfgPath)
	if err == nil {
		err = cfg.LSP.CheckEnv(cfgPath)
	}
	if err == nil && (cfg.Sandbox || cfg.Environment == "container") {
		err = errors.New("lsp can't be combined with --sandbox or --env container: language servers run on the host, outside the sandbox or container; turn lsp off for this run")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+prefix+err.Error())
		return 2
	}
	return 0
}

// newLSP returns the executor's language-server settings, or nil when lsp
// is off. No server starts until a file it handles is used; each server's
// stderr goes to a per-run log under .tinyagent/lsp.
func newLSP(cfg config.AgentConfig, root string) *tools.LSP {
	l := cfg.LSP
	if !l.Enabled {
		return nil
	}
	m := lsp.New(lsp.Options{
		Root:      root,
		Servers:   l.Servers,
		LogDir:    filepath.Join(root, ".tinyagent", "lsp"),
		LogPrefix: fmt.Sprintf("%s-%d-", time.Now().UTC().Format("20060102T150405Z"), os.Getpid()),
	})
	var desc []string
	for _, s := range l.Servers {
		desc = append(desc, fmt.Sprintf("%s (%s)", s.Name, strings.Join(s.Extensions, ", ")))
	}
	fmt.Fprintf(os.Stderr, "lsp: %s\n", strings.Join(desc, "; "))
	return &tools.LSP{
		Manager:   m,
		AfterEdit: l.DiagnosticsAfterEdit,
		Wait:      time.Duration(l.DiagnosticsWaitMS) * time.Millisecond,
		Max:       l.MaxDiagnostics,
		Warnings:  l.Severity == "warning",
	}
}

// newWebFetcher applies --web-fetch-allow-domain to cfg and returns the
// web_fetch tool, or nil when it is off. Its settings come only from the
// user's config, -c and flags; an empty allowlist is a usage error.
func newWebFetcher(fs *flag.FlagSet, o options, cfg *config.AgentConfig) (*tools.WebFetcher, error) {
	if flagWasSet(fs, "web-fetch-allow-domain") {
		if !cfg.WebFetch.Enabled {
			return nil, &config.InvalidError{Err: errors.New("--web-fetch-allow-domain requires --web-fetch (web_fetch.enabled: true)")}
		}
		cfg.WebFetch.AllowedDomains = o.webFetchAllowDomains
	}
	w := cfg.WebFetch
	if !w.Enabled {
		return nil, nil
	}
	if len(w.AllowedDomains) == 0 {
		return nil, &config.InvalidError{Err: errors.New("web_fetch is enabled but web_fetch.allowed_domains is empty; list the hosts it may read in web_fetch.allowed_domains or with --web-fetch-allow-domain")}
	}
	if err := w.Validate(); err != nil {
		return nil, &config.InvalidError{Err: err}
	}
	f, err := tools.NewWebFetcher(w.AllowedDomains, w.MaxBytes, time.Duration(w.TimeoutSeconds)*time.Second, "tinyagent/"+currentVersion())
	if err != nil {
		return nil, &config.InvalidError{Err: err}
	}
	unit := "domains"
	if len(w.AllowedDomains) == 1 {
		unit = "domain"
	}
	fmt.Fprintf(os.Stderr, "web_fetch: %d %s (%s)\n", len(w.AllowedDomains), unit, strings.Join(w.AllowedDomains, ", "))
	return f, nil
}

// dirList is the repeatable --add-dir-read and --add-dir-write flags.
type dirList []string

func (d *dirList) String() string { return strings.Join(*d, ",") }
func (d *dirList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("empty directory")
	}
	*d = append(*d, v)
	return nil
}

// additionalDirs returns the run's validated additional directories: from
// --add-dir-read and --add-dir-write when either is given (replacing
// additional_dirs; relative flag paths are taken from root), else from the
// config. In read-only mode every directory is read mode. Errors are
// config.InvalidError (exit status 2), before any provider request.
func additionalDirs(fs *flag.FlagSet, o options, cfg config.AgentConfig, root string, verbose bool) ([]tools.ExtraDir, error) {
	var in []tools.ExtraDir
	if flagWasSet(fs, "add-dir-read") || flagWasSet(fs, "add-dir-write") {
		for _, p := range o.addDirRead {
			in = append(in, tools.ExtraDir{Path: p, Mode: tools.DirRead})
		}
		for _, p := range o.addDirWrite {
			in = append(in, tools.ExtraDir{Path: p, Mode: tools.DirWrite})
		}
	} else {
		for _, d := range cfg.AdditionalDirs {
			in = append(in, tools.ExtraDir{Path: d.Path, Mode: d.Mode})
		}
	}
	dirs, err := tools.ResolveExtraDirs(root, in, cfg.ReadOnly)
	if err != nil {
		return nil, &config.InvalidError{Err: err}
	}
	if verbose {
		for _, d := range dirs {
			fmt.Fprintf(os.Stderr, "additional dir: %s (%s)\n", d.Path, d.Mode)
		}
	}
	return dirs, nil
}

// warnSnapshotDirs notes that workspace snapshots, and so --rewind, cover
// only --cwd, not write-mode additional directories.
func warnSnapshotDirs(cfg config.AgentConfig, dirs []tools.ExtraDir) {
	if !cfg.Snapshots.Enabled || cfg.ReadOnly {
		return
	}
	for _, d := range dirs {
		if d.Mode == tools.DirWrite {
			fmt.Fprintln(os.Stderr, "warning: snapshots cover only --cwd; --rewind won't restore changes in additional directories")
			return
		}
	}
}

// domainList is the repeatable --sandbox-allow-domain flag.
type domainList []string

func (d *domainList) String() string { return strings.Join(*d, ",") }
func (d *domainList) Set(v string) error {
	if _, err := sandboxnet.ParseRule(v); err != nil {
		return err
	}
	*d = append(*d, v)
	return nil
}

// startEnvironment returns the environment the config asks for and a
// function that releases it. It fails if the environment can't be set up
// (never falling back to an unconfined local one), and otherwise announces
// any sandbox, container or command_env policy on stderr. Read-only runs
// have no command tools, so they get a plain local environment.
func startEnvironment(ctx context.Context, cfg config.AgentConfig, root string, extra []tools.ExtraDir, verbose bool) (tools.Environment, func(), error) {
	if cfg.ReadOnly {
		return tools.Local{}, func() {}, nil
	}
	if cfg.Environment == "container" {
		if cfg.CommandEnv.Configured() {
			fmt.Fprintln(os.Stderr, "command env: not applied; the container environment forwards no host variables")
		}
		c, err := tools.StartContainer(ctx, cfg.Container.Runtime, cfg.Container.Image, cfg.Container.RunArgs, root, extra)
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
	var ce *tools.CommandEnv
	if cfg.CommandEnv.Configured() {
		ce = tools.NewCommandEnv(cfg.CommandEnv, os.Environ())
	}
	if !cfg.Sandbox {
		describeCommandEnv(os.Stderr, ce, verbose)
		return tools.Local{CommandEnv: ce}, func() {}, nil
	}
	writable := append([]string(nil), cfg.SandboxWritablePaths...)
	for _, d := range extra {
		if d.Mode == tools.DirWrite {
			writable = append(writable, d.Path)
		}
	}
	sb, err := tools.NewSandbox(root, writable, cfg.SandboxNetwork, cfg.SandboxAllowedDomains)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintln(os.Stderr, sb.Describe())
	describeCommandEnv(os.Stderr, ce, verbose)
	return tools.Local{Sandbox: sb, CommandEnv: ce}, sb.Cleanup, nil
}

// describeCommandEnv prints the command_env summary and, with verbose, the
// variable names it passed and withheld. Values are never printed.
func describeCommandEnv(w io.Writer, ce *tools.CommandEnv, verbose bool) {
	if ce == nil {
		return
	}
	fmt.Fprintln(w, ce.Describe())
	if !verbose {
		return
	}
	list := func(names []string) string {
		if len(names) == 0 {
			return "(none)"
		}
		return strings.Join(names, " ")
	}
	fmt.Fprintln(w, "command env passed:", list(ce.Passed))
	fmt.Fprintln(w, "command env withheld:", list(ce.Withheld))
	if len(ce.Missing) > 0 {
		fmt.Fprintln(w, "command env pass not set:", list(ce.Missing))
	}
}

// usageGroups lays out --help: each flag's shorthand, long name and value
// placeholder, by group. Help text and defaults come from the FlagSet.
var usageGroups = []struct {
	name  string
	flags [][3]string
}{
	{"", [][3]string{{"h", "help", ""}}},
	{"Task", [][3]string{{"t", "task", "string"}, {"m", "model", "string"}, {"", "reasoning-effort", "level"}, {"", "fallback-model", "spec"}, {"", "no-fallback", ""}, {"", "openai-api", "api"}, {"c", "config", "path"}, {"", "cwd", "dir"}, {"", "worktree", "[=name]"}, {"", "resume", "path"}}},
	{"Limits", [][3]string{{"", "step-limit", "n"}, {"", "cost-limit", "usd"}, {"", "time-limit", "sec"}, {"", "max-format-errors", "n"}, {"", "max-output-tokens", "n"}, {"", "no-compact", ""}}},
	{"Output", [][3]string{{"o", "output", "path"}, {"v", "verbose", ""}, {"", "no-stream", ""}, {"", "json", ""}, {"", "output-schema", "path"}, {"", "log-db", "[=path]"}, {"", "no-agents-md", ""}, {"", "scoped-agents-md", ""}, {"", "read-only", ""}, {"", "add-dir-read", "path"}, {"", "add-dir-write", "path"}, {"", "sandbox", ""}, {"", "sandbox-network", ""}, {"", "sandbox-allow-domain", "host"}, {"", "env", "name"}, {"", "command-env", "mode"}, {"", "snapshots", ""}}},
	{"Rewind", [][3]string{{"", "rewind", "path"}, {"", "to", "target"}, {"", "dry-run", ""}}},
	{"Other", [][3]string{{"V", "version", ""}, {"", "update", ""}, {"", "check", ""}}},
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
	fmt.Fprint(w, "Usage: tinyagent -t \"<prompt>\" [flags]    (or --task \"<prompt>\")\n       echo \"<prompt>\" | tinyagent -t -\n       tinyagent --resume <trajectory.json> --cwd <dir> -o <new.json> [flags]\n       tinyagent --resume <finished.json> -t \"<follow-up>\" --cwd <dir> -o <new.json> [flags]\n       tinyagent --rewind <trajectory.json> --cwd <dir> [--to start|<step>|<id>] [--dry-run]\n       tinyagent --check [-m <model>] [-c <config>] [--cwd <dir>]\n\n")
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
		"  tinyagent --resume run.json --cwd . --step-limit 60 -o continued.json\n"+
		"  tinyagent --resume review.json -t \"fix the races you found\" --cwd . -o fix.json\n"+
		"  tinyagent --rewind run.json --cwd . --to 3 --dry-run\n"+
		"  tinyagent --check -m anthropic/claude-sonnet-4\n")
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
	if n := len(t.ModelSwitches); n > 0 && t.ActiveModel != "" {
		noun := "fallbacks"
		if n == 1 {
			noun = "fallback"
		}
		s += fmt.Sprintf(", %d model %s (now %s)", n, noun, t.ActiveModel)
	}
	if t.FormatErrors > 0 {
		s += fmt.Sprintf(", %d format errors", t.FormatErrors)
	}
	if t.Truncations > 0 {
		s += fmt.Sprintf(", %d truncated responses", t.Truncations)
	}
	if n := len(t.Subagents); n > 0 {
		s += fmt.Sprintf(", %d subagent runs (%d steps)", n, t.SubagentSteps)
	}
	if t.ExitStatus == agent.StatusError {
		s += ": " + t.Submission
	}
	if t.ExitStatus == agent.StatusFormatError && t.LastFormatError != "" {
		s += ": " + t.LastFormatError
	}
	if t.ExitStatus == agent.StatusVerificationFailed && t.StopReason != "" {
		s += ": " + strings.Join(strings.Fields(t.StopReason), " ")
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
	case o.maxOutputTokens < 0:
		return "--max-output-tokens must be a whole number >= 0 (0 = provider default)"
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
	if len(a.FallbackModels) > 0 {
		started["fallback_models"] = a.FallbackModels
	}
	if a.OpenAIAPI != "" {
		started["openai_api"] = a.OpenAIAPI
	}
	if a.ProviderAPI != "" {
		started["provider_api"] = a.ProviderAPI
		started["provider_base_url"] = a.ProviderBaseURL
	}
	if a.FollowUp != "" {
		started["follow_up"] = true
	}
	if l, ok := a.Tools.Env.(tools.Local); ok {
		if ce := agent.CommandEnvInfo(l.CommandEnv); ce != nil {
			started["command_env"] = ce
		}
	}
	if fa := agent.FileAccessInfo(a.Tools.FileAccess); fa != nil {
		started["file_access"] = fa
	}
	if dirs := agent.AdditionalDirsInfo(a.Tools.ExtraDirs, a.Tools.ReadOnly); len(dirs) > 0 {
		started["additional_dirs"] = dirs
	}
	if wf := a.Tools.WebFetch; wf != nil {
		started["web_fetch"] = map[string]any{"allowed_domains": wf.Domains}
	}
	if l := a.Tools.LSP; l != nil {
		started["lsp"] = map[string]any{"servers": l.Manager.Names()}
	}
	if a.ScopedAgentsMD && !a.SkipAgentsMD {
		delivered := []string{}
		if resumed {
			delivered = append(delivered, agent.ScopedAgentsMDDelivered(a.Resume)...)
		}
		started["scoped_agents_md_enabled"] = true
		started["scoped_agents_md"] = delivered
	}
	if wt := a.Worktree; wt != nil {
		started["worktree"] = map[string]any{"path": wt.Path, "branch": wt.Branch, "base_commit": wt.BaseCommit, "repo_root": wt.RepoRoot}
	}
	events.emit("run.started", started)
	a.OnEvent = func(kind string, fields map[string]any) {
		events.emit(kind, fields)
		if events.err != nil {
			stop()
		}
	}
	for _, spec := range append([]string{a.ModelName}, a.FallbackModels...) {
		if a.Config.CostLimit > 0 && model.Unpriced(spec) {
			events.emit("warning", map[string]any{"message": fmt.Sprintf("no pricing available for %s; --cost-limit cannot be enforced", spec)})
		}
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

// toolOutputFiles returns the store for over-limit tool output, or nil when
// the feature is off. --tool-output-files overrides tool_output_files.enabled.
// The directory is checked even when the feature is off, so a bad setting
// fails at startup rather than when it is first turned on.
func toolOutputFiles(fs *flag.FlagSet, o options, cfg config.AgentConfig, root string) (*tools.OutputFiles, error) {
	on := cfg.ToolOutputFiles.Enabled
	if flagWasSet(fs, "tool-output-files") {
		on = o.toolOutputFiles
	}
	if _, err := tools.ResolveOutputDir(root, cfg.ToolOutputFiles.Dir); err != nil {
		return nil, err
	}
	if !on {
		return nil, nil
	}
	return tools.NewOutputFiles(root, cfg.ToolOutputFiles.Dir, time.Now())
}

// newFileAccess compiles the file_access rules (nil when there are none),
// rejecting a deny_read rule that would hide the tool output directory the
// model is told to read. With verbose it prints the rule counts.
func newFileAccess(cfg config.AgentConfig, root string, outputFiles *tools.OutputFiles, verbose bool) (*tools.FileAccess, error) {
	fa, err := tools.NewFileAccess(root, cfg.FileAccess.DenyRead, cfg.FileAccess.DenyWrite)
	if err != nil {
		return nil, &config.InvalidError{Err: err}
	}
	if fa == nil {
		return nil, nil
	}
	if outputFiles != nil {
		dir := outputFiles.Dir
		if fa.Hidden(dir) || fa.Hidden(filepath.Join(dir, "run", "call.txt")) {
			return nil, &config.InvalidError{Err: fmt.Errorf("file_access.deny_read hides the tool output directory %s; the model could not read its saved output", dir)}
		}
	}
	if verbose {
		r, w := fa.Counts()
		fmt.Fprintf(os.Stderr, "file_access: %d deny_read and %d deny_write rules (file tools only; bash, start_process, hooks and MCP servers are not restricted)\n", r, w)
	}
	return fa, nil
}

// startOutputFiles routes save failures to warn and prunes old run
// directories before the run saves anything.
func startOutputFiles(o *tools.OutputFiles, maxRuns int, warn func(string)) {
	if o == nil {
		return
	}
	o.Warn = warn
	if err := o.Prune(maxRuns); err != nil {
		warn("tool_output_files: pruning old runs: " + err.Error())
	}
}

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
	var vals []string
	for _, kv := range envs {
		if _, v, ok := strings.Cut(kv, "="); ok {
			vals = append(vals, v)
		}
	}
	// Configured providers read their key and secret headers from the
	// environment; redact those values too.
	vals = append(vals, model.ProviderSecrets()...)
	// Hook env entries name variables forwarded from tinyagent's own
	// environment; redact the values they had at startup.
	var hookDefs []config.Hook
	hookDefs = append(hookDefs, cfg.Hooks.PreTool...)
	hookDefs = append(hookDefs, cfg.Hooks.PostTool...)
	hookDefs = append(hookDefs, cfg.Hooks.Stop...)
	for _, h := range hookDefs {
		for _, name := range h.Env {
			if _, v, ok := strings.Cut(name, "="); ok {
				vals = append(vals, v)
			} else if v := os.Getenv(name); v != "" {
				vals = append(vals, v)
			}
		}
	}
	// command_env.set values may be secrets the user deliberately provides.
	for _, v := range cfg.CommandEnv.Set {
		vals = append(vals, v)
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
		"truncations":   t.Truncations,
	}
	if t.ExitStatus == agent.StatusSubmitted && t.StructuredOutput != nil {
		fields["structured_output"] = t.StructuredOutput
	}
	if t.StopContinuations > 0 {
		fields["stop_continuations"] = t.StopContinuations
	}
	if len(t.Subagents) > 0 {
		fields["subagents"] = len(t.Subagents)
		fields["subagent_steps"] = t.SubagentSteps
	}
	if t.StopReason != "" {
		fields["stop_reason"] = t.StopReason
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

// checkMaxOutputTokens rejects a negative budget, or a budget for a
// provider that has no per-request output limit, before any request.
func checkMaxOutputTokens(spec string, n int) error {
	if n < 0 {
		return fmt.Errorf("--max-output-tokens/max_output_tokens must be a whole number >= 0, got %d", n)
	}
	if n == 0 {
		return nil
	}
	if err := model.MaxOutputTokensSupported(spec); err != nil {
		return fmt.Errorf("--max-output-tokens/max_output_tokens: %w", err)
	}
	return nil
}

// exitOutputTruncated is the exit code of an OutputTruncated run.
const exitOutputTruncated = 3

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
	if _, ok := model.ConfiguredProvider(spec); ok {
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
