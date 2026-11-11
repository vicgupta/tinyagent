package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/telemetry"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// oneShot carries a one-shot (-t or review) run's state between its setup
// phases (issue #416). The phases run in a fixed order, the order run has
// always used, so the same flags and config produce the same messages,
// errors and exit codes.
type oneShot struct {
	o      *options
	fs     *flag.FlagSet
	review *reviewTarget

	// prepareTask
	task     string
	schema   *outschema.Schema
	progress io.Writer

	// loadConfig and applyFlags
	cfgSrc     config.Source
	cfg        config.AgentConfig
	cfgPath    string
	taskImages []model.Image
	hookRunner *hooks.Runner
	policy     *permissions.Policy
	otelCfg    *telemetry.Settings

	// setupFeatures
	webFetch  *tools.WebFetcher
	webSearch *tools.WebSearcher
	lock      *lockdown.State

	// resolveModels
	openaiAPI string
	fallbacks []string
	helpers   map[string]model.Model
	subagents *agent.Subagents
	workers   *agent.Workers

	// setupWorkspace
	root        string
	worktree    *trajectory.WorktreeInfo
	outputFiles *tools.OutputFiles
	fileAccess  *tools.FileAccess
	extraDirs   []tools.ExtraDir
	rules       *ratchetRun
	reviewInfo  *trajectory.ReviewInfo
	cmdTools    []*tools.CommandTool
}

// runOneShot runs one task (or review) to completion and returns the exit
// code.
func runOneShot(o *options, fs *flag.FlagSet, review *reviewTarget) int {
	r := &oneShot{o: o, fs: fs, review: review}
	phases := []func() (int, bool){r.prepareTask, r.loadConfig, r.applyFlags, r.setupFeatures, r.resolveModels, r.setupWorkspace}
	for _, phase := range phases {
		if code, done := phase(); done {
			return code
		}
	}
	return r.execute()
}

// prepareTask reads the task (optional focus in review mode) and the
// output schema, and picks where progress text goes.
func (r *oneShot) prepareTask() (int, bool) {
	o := r.o
	if o.task == "" && len(o.imagePaths) > 0 && r.review == nil {
		fmt.Fprintln(os.Stderr, "error: --image requires a task (-t); images are attached to the task message")
		return 2, true
	}
	if o.task == "" && r.review == nil {
		fmt.Fprintln(os.Stderr, "error: --task is required")
		r.fs.Usage()
		return 2, true
	}
	// In review mode the task is optional focus for the review.
	if o.task != "" {
		task, err := readTask(o.task, os.Stdin)
		if err != nil {
			return fail(err), true
		}
		r.task = task
	}
	if o.outputSchema != "" {
		schema, err := outschema.Load(o.outputSchema)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2, true
		}
		r.schema = schema
	}
	// With a schema, stdout carries only the validated result (or JSONL), so
	// progress text goes to stderr.
	r.progress = io.Writer(os.Stdout)
	if r.schema != nil {
		r.progress = os.Stderr
	}
	return 0, false
}

// loadConfig loads the config (and its memory settings), registers its
// providers, and loads the task images, hooks, permission policy and
// OpenTelemetry settings.
func (r *oneShot) loadConfig() (int, bool) {
	o := r.o
	cfgSrc, cfg, err := loadConfig(o, o.cwd, os.Stderr)
	r.cfgSrc, r.cfgPath = cfgSrc, cfgSrc.Path
	if err == nil {
		err = applyMemoryConfig(&cfg, o.cwd, os.Stderr)
	}
	if err != nil {
		return loadConfigError(err), true
	}
	r.cfg = cfg
	if o.verbose {
		fmt.Fprintln(os.Stderr, "config:", cfgSrc.Label())
	}
	registerProviders(r.cfg)
	if r.taskImages, err = loadTaskImages(o.imagePaths, r.cfg.MaxImageBytes); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2, true
	}
	announceTaskImages(os.Stderr, r.taskImages, o.verbose)
	if r.hookRunner, err = hooks.New(r.cfg.Hooks, r.cfgPath); err != nil {
		return fail(inConfig(r.cfgPath, err)), true
	}
	if r.policy, err = permissions.New(r.cfg.Permissions); err != nil {
		return configFail(inConfig(r.cfgPath, err)), true
	}
	if r.otelCfg, err = otelSettings(r.fs, *o, r.cfg); err != nil {
		return configFail(err), true
	}
	return 0, false
}

// applyFlags applies the command-line overrides of limits, read-only,
// tool scope, sandbox, verify, plan, images, apply_patch, subagents,
// workers and ask_user to the config.
func (r *oneShot) applyFlags() (int, bool) {
	o, fs, cfg := r.o, r.fs, &r.cfg
	applyFlagOverrides(fs, cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "max-format-errors") {
		cfg.MaxFormatErrors = o.maxFormatErrors
	}
	if flagWasSet(fs, "max-output-tokens") {
		cfg.MaxOutputTokens = o.maxOutputTokens
	}
	if flagWasSet(fs, "read-only") {
		cfg.ReadOnly = o.readOnly
	}
	if _, err := resolveScopedTools(fs, *o, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2, true
	}
	if r.review != nil {
		cfg.ReadOnly = true
	}
	applySandboxFlags(fs, cfg, *o)
	if err := applyVerifyFlags(fs, cfg, o.verify); err != nil {
		return fail(err), true
	}
	if err := applyPlanFlags(fs, *o, cfg); err != nil {
		return fail(err), true
	}
	applyToolFlags(fs, *o, cfg)
	if msg := askUserConflict(*cfg, r.schema != nil, o.output); msg != "" {
		fmt.Fprintln(os.Stderr, "error: "+msg)
		return 2, true
	}
	return 0, false
}

// applyPlanFlags applies --plan, --plan-first and checks --plan-file.
func applyPlanFlags(fs *flag.FlagSet, o options, cfg *config.AgentConfig) error {
	if flagWasSet(fs, "plan") {
		cfg.Plan.Enabled = o.plan
	}
	if flagWasSet(fs, "plan-first") {
		cfg.Plan.RequireFirst = o.planFirst
	}
	if cfg.Plan.RequireFirst && flagWasSet(fs, "plan") && !o.plan {
		return errors.New("--plan-first needs the plan tool; drop --plan=false")
	}
	if cfg.Plan.RequireFirst {
		cfg.Plan.Enabled = true
	}
	if o.planFile != "" && !cfg.Plan.Enabled {
		return errors.New("--plan-file requires the plan tool; pass --plan or set plan.enabled: true")
	}
	return nil
}

// applyToolFlags applies --images, --apply-patch, --subagents, --workers
// and --ask-user.
func applyToolFlags(fs *flag.FlagSet, o options, cfg *config.AgentConfig) {
	if flagWasSet(fs, "images") {
		cfg.Images.Enabled = o.images
	}
	if flagWasSet(fs, "apply-patch") {
		cfg.ApplyPatch = o.applyPatch
	}
	if flagWasSet(fs, "subagents") {
		cfg.Subagents.Enabled = o.subagents
	}
	if flagWasSet(fs, "workers") {
		setWorkers(cfg, o.workers)
	}
	if flagWasSet(fs, "ask-user") {
		cfg.AskUser.Enabled = o.askUser
	}
}

// setupFeatures sets up web_fetch, web_search, plugins, LSP, scoped
// AGENTS.md, skills, memory, handoff, lockdown and snapshots.
func (r *oneShot) setupFeatures() (int, bool) {
	o, fs, cfg := r.o, r.fs, &r.cfg
	var err error
	if r.webFetch, err = newWebFetcher(fs, *o, cfg); err != nil {
		return configFail(err), true
	}
	if r.webSearch, err = newWebSearcher(fs, *o, cfg); err != nil {
		return configFail(err), true
	}
	if err := checkPlugins(cfg, o.cwd, os.Stderr, o.verbose, true); err != nil {
		return configFail(err), true
	}
	if flagWasSet(fs, "lsp") {
		cfg.LSP.Enabled = o.lsp
	}
	if flagWasSet(fs, "scoped-agents-md") {
		cfg.ScopedAgentsMD = o.scopedAgentsMD
	}
	if code := checkLSP(*cfg, r.cfgPath, ""); code != 0 {
		return code, true
	}
	if _, _, err := skillsFlag(fs, *o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2, true
	}
	if flagWasSet(fs, "memory") {
		cfg.Memory.Enabled = o.memory.enabled
	}
	if flagWasSet(fs, "handoff") {
		cfg.Handoff.Enabled = o.handoff
	}
	lock, code := setupLockdown(fs, o.lockdown, cfg, r.review != nil, false, os.Stderr)
	if code != 0 {
		return code, true
	}
	r.lock = lock
	skillsDec := applySkillsDefault(fs, *o, cfg, r.cfgSrc, skillsMode{
		Interactive: runIsInteractive(),
		Hardened:    hardenedReason(false, false, cfg.UntrustedContent.Enabled, cfg.ReadOnly),
	})
	if o.verbose {
		fmt.Fprintln(os.Stderr, "skills:", skillsDec)
	}
	return r.checkSnapshotsAndEnv()
}

// checkSnapshotsAndEnv applies --snapshots and checks the snapshot and
// environment settings.
func (r *oneShot) checkSnapshotsAndEnv() (int, bool) {
	if flagWasSet(r.fs, "snapshots") {
		r.cfg.Snapshots.Enabled = r.o.snapshots
	}
	if code := checkSnapshots(&r.cfg, r.o.output); code != 0 {
		return code, true
	}
	if msg := envConflict(r.cfg); msg != "" {
		fmt.Fprintln(os.Stderr, "error: "+msg)
		return 2, true
	}
	return 0, false
}

// resolveModels resolves the main model and its reasoning effort, output
// budget and OpenAI transport, checks the provider environment, and builds
// the fallbacks, helper models, subagents and workers.
func (r *oneShot) resolveModels() (int, bool) {
	r.o.modelName = resolveModel(r.fs, r.o.modelName, r.cfg.Model)
	if err := r.checkModelSettings(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2, true
	}
	if err := r.buildModelRoles(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2, true
	}
	model.DefaultRuntime.PriceOverride = [2]float64{r.cfg.InputPricePerMTok, r.cfg.OutputPricePerMTok}
	return 0, false
}

// checkModelSettings applies --reasoning-effort and --openai-api and checks
// them, the output budget and the provider's environment.
func (r *oneShot) checkModelSettings() error {
	o, fs, cfg := r.o, r.fs, &r.cfg
	if flagWasSet(fs, "reasoning-effort") {
		cfg.ReasoningEffort = o.reasoningEffort
	}
	if err := checkReasoningEffort(o.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := checkMaxOutputTokens(o.modelName, cfg.MaxOutputTokens); err != nil {
		return err
	}
	if flagWasSet(fs, "openai-api") {
		cfg.OpenAIAPI = o.openaiAPI
	}
	if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
		return err
	}
	r.openaiAPI = model.OpenAITransport(o.modelName, cfg.OpenAIAPI)
	return model.CheckProviderEnv(o.modelName)
}

// buildModelRoles resolves the fallback chain and builds the helper
// models, subagents and workers.
func (r *oneShot) buildModelRoles() error {
	o, cfg := r.o, r.cfg
	fallbacks, err := fallbackModels(r.fs, o.fallbackModels, o.noFallback, cfg)
	if err == nil {
		err = checkFallbacks(o.modelName, fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		return err
	}
	r.fallbacks = fallbacks
	if r.helpers, err = newHelpers(cfg, o.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if r.subagents, err = newSubagents(cfg, o.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := checkWorkers(cfg, r.review != nil, o.cwd); err != nil {
		return err
	}
	r.workers, err = newWorkers(cfg, o.modelName, cfg.ReasoningEffort)
	return err
}

// setupWorkspace resolves the root (or creates the --worktree), the tool
// output files, file access, additional directories and project rules,
// collects the review change set, and loads the command tools.
func (r *oneShot) setupWorkspace() (int, bool) {
	if code, done := r.setupRoot(); done {
		return code, true
	}
	if code, done := r.setupFileAccess(); done {
		return code, true
	}
	if r.review != nil {
		var err error
		r.task, r.reviewInfo, err = collectReview(r.review, r.root, r.task, r.fileAccess, os.Stderr, r.o.verbose)
		if errors.Is(err, errNothingToReview) {
			fmt.Fprintln(os.Stderr, "nothing to review")
			return 0, true
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2, true
		}
	}
	cmdTools, err := commandTools(os.Stderr, r.cfg, r.root)
	if err != nil {
		err = inConfig(r.cfgPath, err)
		fmt.Fprintln(os.Stderr, "error:", err)
		printSetupHint(os.Stderr, err)
		return 2, true
	}
	r.cmdTools = cmdTools
	if r.cfg.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}
	return 0, false
}

// setupRoot resolves --cwd to a directory and creates the --worktree.
func (r *oneShot) setupRoot() (int, bool) {
	o := r.o
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return fail(err), true
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("--cwd %s is not a directory", o.cwd)), true
	}
	r.root = root
	if o.worktree.on {
		if r.root, r.worktree, err = setupWorktree(root, o.worktree.name, os.Stderr, o.verbose); err != nil {
			if errors.Is(err, errInterruptedSetup) {
				return fail(err), true
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2, true
		}
	}
	return 0, false
}

// setupFileAccess sets up the tool output files, file access, additional
// directories and the project rules, recompiling the permission policy
// when the rules enforce any.
func (r *oneShot) setupFileAccess() (int, bool) {
	o := r.o
	var err error
	if r.outputFiles, err = toolOutputFiles(r.fs, *o, r.cfg, r.root); err != nil {
		return fail(err), true
	}
	if r.fileAccess, err = newFileAccess(r.cfg, r.root, r.outputFiles, o.verbose); err != nil {
		return configFail(err), true
	}
	if r.extraDirs, err = additionalDirs(r.fs, *o, r.cfg, r.root, o.verbose); err != nil {
		return configFail(err), true
	}
	r.fileAccess.AddDirs(r.extraDirs)
	if r.rules, err = loadRatchet(o.ratchet.off, r.root, &r.cfg); err != nil {
		return configFail(err), true
	}
	if r.rules != nil && r.rules.enforced > 0 {
		if r.policy, err = permissions.New(r.cfg.Permissions); err != nil {
			return configFail(err), true
		}
	}
	return 0, false
}

// execute starts the run's resources (LSP, snapshots, the command
// environment, MCP and plugin tools), builds the model and the agent, runs
// the task and maps the result to an exit code. The resources are closed
// when it returns, in reverse order.
func (r *oneShot) execute() int {
	o := r.o
	lspTools := newLSP(r.cfg, r.root)
	if lspTools != nil {
		defer lspTools.Manager.Close()
	}
	warnSnapshotDirs(r.cfg, r.extraDirs)
	snaps, err := openSnapshots(r.fs, *o, r.cfg, r.root, o.output)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, closeEnv, err := startEnvironment(ctx, r.cfg, r.root, r.extraDirs, o.verbose)
	if err != nil {
		return fail(err)
	}
	defer closeEnv()
	// MCP discovery counts against the wall-clock limit. Agent.Run applies
	// the same limit again, and the earlier deadline set here wins.
	if r.cfg.WallTimeSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(r.cfg.WallTimeSec)*time.Second)
		defer cancel()
	}
	mcpRegistry, err := mcptools.Start(ctx, r.root, mcpServers(os.Stderr, r.cfg), os.Stderr)
	if err != nil {
		return configFail(err)
	}
	defer mcpRegistry.Close()
	extSpecs, extCall, err := externalTools(mcpRegistry, startWasmTools(ctx, r.cfg, r.root, r.fileAccess, r.lock, os.Stderr))
	if err != nil {
		return configFail(err)
	}
	if err := checkCommandToolNames(r.cmdTools, extSpecs); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if o.verbose {
		mcpRegistry.Log = os.Stderr
	}
	deltas := &textDeltas{}
	m, code, done := r.newModel(deltas)
	if done {
		return code
	}
	a, code, done := r.newAgent(m, toolWiring{
		root:        r.root,
		env:         env,
		stream:      streamTo(jsonStream(o.jsonOutput), r.progress),
		cmdTools:    r.cmdTools,
		commandLog:  commandLog(o.verbose),
		extSpecs:    extSpecs,
		extCall:     extCall,
		mcp:         mcpRegistry,
		subagents:   r.subagents,
		workers:     r.workers,
		webFetch:    r.webFetch,
		webSearch:   r.webSearch,
		lsp:         lspTools,
		outputFiles: r.outputFiles,
		fileAccess:  r.fileAccess,
		extraDirs:   r.extraDirs,
	}, snaps)
	if done {
		return code
	}
	events, warn, setupWarnings, code, done := r.openEvents(a, stop, deltas)
	if done {
		return code
	}
	defer events.close()
	handoffState := r.prepareAgent(ctx, a, events, warn, setupWarnings)
	return r.runAgent(ctx, a, events, handoffState, warn)
}

// textSink returns the streaming callback for model text: none with
// --no-stream, JSONL deltas with --json, else the progress writer.
func (r *oneShot) textSink(deltas *textDeltas) func(string) {
	switch {
	case r.o.noStream:
		return nil
	case r.o.jsonOutput:
		return deltas.write
	}
	return func(s string) { io.WriteString(r.progress, s) }
}

// newModel builds the main model with its reasoning effort, output budget
// and fallbacks, announcing the transport, profile and provider first.
func (r *oneShot) newModel(deltas *textDeltas) (model.Model, int, bool) {
	o, cfg := r.o, r.cfg
	onText := r.textSink(deltas)
	if r.openaiAPI != "" && o.verbose {
		fmt.Fprintf(os.Stderr, "openai transport: %s\n", r.openaiAPI)
	}
	announceProfile(os.Stderr, o.profile, r.cfgPath, o.verbose)
	announceProvider(os.Stderr, o.modelName, o.verbose)
	m, err := model.NewWith(o.modelName, onText, model.Options{OpenAIAPI: r.openaiAPI, Handoff: model.HandoffOptions{Dir: r.root, ReadOnly: cfg.ReadOnly}})
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, o.modelName, err.Error())
		return nil, code, true
	}
	if err := model.SetReasoningEffort(m, o.modelName, cfg.ReasoningEffort); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, 2, true
	}
	if err := model.SetMaxOutputTokens(m, o.modelName, cfg.MaxOutputTokens); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, 2, true
	}
	warnUnpriced(os.Stderr, o.modelName, cfg.CostLimit)
	if m, err = withFallbacks(m, o.modelName, r.fallbacks, onText, cfg, cfg.ReasoningEffort, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, 2, true
	}
	return m, 0, false
}

// newAgent applies --redact-secrets, builds the secret redactor and the
// tool executor from wiring, and creates the agent.
func (r *oneShot) newAgent(m model.Model, wiring toolWiring, snaps agent.Snapshotter) (*agent.Agent, int, bool) {
	o := r.o
	providerAPI, providerBaseURL := providerInfo(o.modelName)
	if flagWasSet(r.fs, "redact-secrets") {
		r.cfg.RedactSecrets.Enabled = o.redactSecrets
	}
	redactor, redactInfo := newSecretRedactor(r.cfg, os.Environ(), os.Stderr, o.verbose)
	wiring.redactor = redactor
	cfg := r.cfg
	a, err := agent.New(agent.Options{
		Model:           m,
		ModelName:       o.modelName,
		FallbackModels:  r.fallbacks,
		ReasoningEffort: cfg.ReasoningEffort,
		OpenAIAPI:       r.openaiAPI,
		Profile:         o.profile,
		ProviderAPI:     providerAPI,
		ProviderBaseURL: providerBaseURL,
		Tools:           newRunExecutor(cfg, wiring),
		RedactSecrets:   redactInfo,
		Config:          cfg,
		Hooks:           r.hookRunner,
		Policy:          r.policy,
		Out:             outputTo(runOutput(o.jsonOutput), r.progress),
		Err:             os.Stderr,
		Verbose:         o.verbose,
		Streamed:        !o.noStream,
		SkipAgentsMD:    o.noAgentsMD,
		ScopedAgentsMD:  cfg.ScopedAgentsMD && !o.noAgentsMD,
		NoCompact:       o.noCompact,
		OutputSchema:    r.schema,
		Snapshots:       snaps,
		Subagents:       r.subagents,
		Workers:         r.workers,
		Routes:          modelRoutes(cfg, o.modelName, r.subagents, r.workers),
		Helpers:         r.helpers,
		Worktree:        r.worktree,
		Review:          r.reviewInfo,
		TaskImages:      r.taskImages,
		Lockdown:        r.lock,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, 2, true
	}
	if r.policy != nil && !o.jsonOutput {
		a.PermissionApprover = terminalPermissionApprover
	}
	return a, 0, false
}

// openEvents opens the event sinks (JSONL, the log database,
// OpenTelemetry), records the plugins and checks the tool set. The
// returned warnings (log database and OpenTelemetry setup) are reported
// once the caller has deferred events.close.
func (r *oneShot) openEvents(a *agent.Agent, stop func(), deltas *textDeltas) (*runEvents, func(string), []string, int, bool) {
	o, cfg := r.o, r.cfg
	var events *runEvents
	warn := func(msg string) { events.warn(msg) }
	db, dbErr := openLogDB(r.fs, *o, cfg, r.root, r.task, o.modelName, false, a, warn)
	tr, trWarn := startOTel(r.otelCfg, cfg, a, r.task, false, o.verbose)
	a.Plugins = pluginRecords(cfg, r.root, !cfg.ReadOnly)
	if err := validateRunTools(a.Tools); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, nil, nil, 2, true
	}
	describeScopedTools(os.Stderr, a.Tools.Only)
	events = startEvents(os.Stdout, o.jsonOutput, a, false, stop, db, tr)
	deltas.events = events
	streamToolProgress(a, *o, streamTo(jsonStream(o.jsonOutput), r.progress), os.Stderr)
	if events.err != nil {
		events.close()
		return nil, nil, nil, 1, true
	}
	var setupWarnings []string
	if dbErr != nil {
		setupWarnings = append(setupWarnings, dbErr.Error())
	}
	if trWarn != "" {
		setupWarnings = append(setupWarnings, trWarn)
	}
	return events, warn, setupWarnings, 0, false
}

// prepareAgent reports the setup warnings and starts the per-run
// features: tool output files, project rules, skills, memory, handoff and
// the checkpoint (or plan export) callback.
func (r *oneShot) prepareAgent(ctx context.Context, a *agent.Agent, events *runEvents, warn func(string), setupWarnings []string) *handoffRun {
	cfg := r.cfg
	for _, w := range setupWarnings {
		warn(w)
	}
	startOutputFiles(r.outputFiles, cfg.ToolOutputFiles.MaxRuns, warn)
	r.rules.start(a, events, warn)
	if cfg.SkillsOn() {
		loadSkills(a, r.root, pluginSkillSources(cfg), warn)
	}
	if cfg.Memory.Enabled {
		loadMemory(a, cfg.Memory, r.root, warn)
		events.emit("memory.loaded", agent.MemoryLoadedFields(a.Memory))
		a.Tools.Remember = memoryWriter(a, cfg.Memory)
		a.Tools.MemoryTools = a.Tools.Remember != nil
	}
	handoffState := startHandoff(ctx, cfg, r.root, a, events, warn)
	if onStep := checkpointCallback(a, r.o.output, r.o.planFile); onStep != nil {
		a.OnStep = onStep
	}
	return handoffState
}

// checkpointCallback returns the OnStep callback: save the trajectory to
// output (a failed save warns and the run goes on), or with only planFile
// export the plan, or nil.
func checkpointCallback(a *agent.Agent, output, planFile string) func(*trajectory.Trajectory) error {
	if output != "" {
		return func(t *trajectory.Trajectory) error {
			if err := t.Save(output); err != nil {
				fmt.Fprintln(os.Stderr, "warning: saving trajectory:", err)
				if a.OnEvent != nil {
					a.OnEvent("warning", map[string]any{"message": "saving trajectory: " + err.Error()})
				}
				return nil
			}
			checkpointSaved(a, t, output, planFile)
			return nil
		}
	}
	if planFile != "" {
		return func(t *trajectory.Trajectory) error {
			exportPlan(a, t, planFile)
			return nil
		}
	}
	return nil
}

// runAgent runs the task, reports the summary and returns the exit code.
func (r *oneShot) runAgent(ctx context.Context, a *agent.Agent, events *runEvents, handoffState *handoffRun, warn func(string)) int {
	o := r.o
	startLockdown(a, r.lock, os.Stderr, events)
	start := time.Now()
	t := a.Run(ctx, r.task)
	elapsed := time.Since(start)
	resume := ""
	if !o.jsonOutput {
		resume = resumeCommand(t, o.output, r.root, r.cfg)
	}
	handoffState.finish(t, o.modelName, o.output, events, warn)
	reportSummary(os.Stderr, o.modelName, t, elapsed, resume)
	if r.worktree != nil {
		fmt.Fprintf(os.Stderr, "worktree kept: %s (branch %s)\n", r.worktree.Path, r.worktree.Branch)
	}
	if events.finish(t, elapsed) != nil {
		return 1
	}
	if r.schema != nil {
		return structuredExitCode(t, o.jsonOutput)
	}
	return exitCode(t, o.jsonOutput)
}

// structuredExitCode maps an --output-schema run's result to the exit code
// and, unless stdout carries JSONL, prints the validated result.
func structuredExitCode(t *trajectory.Trajectory, jsonOutput bool) int {
	if t.ExitStatus == agent.StatusOutputTruncated {
		return exitOutputTruncated
	}
	if t.ExitStatus != agent.StatusSubmitted || t.StructuredOutput == nil {
		return 1
	}
	if !jsonOutput {
		if _, err := os.Stdout.Write(append(append([]byte(nil), t.StructuredOutput...), '\n')); err != nil {
			return fail(err)
		}
	}
	return 0
}
