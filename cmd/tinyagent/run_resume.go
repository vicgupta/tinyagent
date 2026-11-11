package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/resume"
	"github.com/vicgupta/tinyagent/internal/telemetry"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// resumeRun carries a --resume run's state between its setup phases
// (issue #416). The phases run in a fixed order, the order runResume has
// always used, so the same flags and checkpoint produce the same messages,
// errors and exit codes.
type resumeRun struct {
	o  *options
	fs *flag.FlagSet

	// preflight
	src      *trajectory.Trajectory
	asked    bool
	finished bool
	followUp string

	// loadConfig
	cfg        config.AgentConfig
	cfgPath    string
	modelName  string
	fallbacks  []string
	taskImages []model.Image
	hookRunner *hooks.Runner
	policy     *permissions.Policy
	otelCfg    *telemetry.Settings
	lock       *lockdown.State

	// setupFeatures and setupWorkspace
	root        string
	outputFiles *tools.OutputFiles
	fileAccess  *tools.FileAccess
	extraDirs   []tools.ExtraDir
	cmdTools    []*tools.CommandTool
	webFetch    *tools.WebFetcher
	webSearch   *tools.WebSearcher

	// newModel
	openaiAPI string
	helpers   map[string]model.Model
	subagents *agent.Subagents
	workers   *agent.Workers
}

// runResume continues an unfinished run from a validated checkpoint. It reuses
// the saved task and model, requires a matching working directory and a
// distinct output destination, and refuses to continue if the effective
// instructions or tool set have changed. The phases live in run_resume.go.
func runResume(o *options, fs *flag.FlagSet) int {
	r := &resumeRun{o: o, fs: fs}
	phases := []func() (int, bool){r.preflight, r.loadConfig, r.applyFlags, r.setupWorkspace}
	for _, phase := range phases {
		if code, done := phase(); done {
			return code
		}
	}
	return r.execute()
}

// preflight loads the checkpoint, resolves the follow-up task (a finished
// checkpoint continues only with one; a pending question is answered with
// -t), and checks that the model and reasoning effort match the
// checkpoint's.
func (r *resumeRun) preflight() (int, bool) {
	o, fs := r.o, r.fs
	taskSet := flagWasSet(fs, "task", "t")
	if !flagWasSet(fs, "cwd") {
		return fail(errors.New("resume: an explicit --cwd matching the checkpoint root is required")), true
	}
	src, err := resume.Load(o.resume)
	if err != nil {
		return fail(fmt.Errorf("resume: %w", err)), true
	}
	r.src = src
	// A finished checkpoint continues only with a follow-up task; an
	// unfinished one only without.
	r.finished = trajectory.Finished(src.ExitStatus)
	r.asked = src.ExitStatus == agent.StatusNeedsInput && src.PendingQuestion != nil
	if code, done := r.resolveFollowUp(taskSet); done {
		return code, true
	}
	// The saved model wins unless an explicit, different model is given.
	if flagWasSet(fs, "model", "m") && o.modelName != src.Model {
		return fail(fmt.Errorf("resume: --model %q conflicts with the checkpoint's model %q", o.modelName, src.Model)), true
	}
	r.modelName = src.Model
	// Like the model, the recorded effort is kept for the whole run (it is
	// part of the provider's prompt cache key); the config value is ignored.
	if flagWasSet(fs, "reasoning-effort") && o.reasoningEffort != src.ReasoningEffort {
		return fail(fmt.Errorf("resume: --reasoning-effort %q conflicts with the checkpoint's reasoning_effort %q", o.reasoningEffort, src.ReasoningEffort)), true
	}
	return 0, false
}

// resolveFollowUp reads the follow-up task, or the answer to the
// checkpoint's pending question, and rejects the --task and --image
// combinations the checkpoint's state doesn't allow.
func (r *resumeRun) resolveFollowUp(taskSet bool) (int, bool) {
	o, src := r.o, r.src
	var err error
	switch {
	case r.asked && len(o.imagePaths) > 0:
		fmt.Fprintln(os.Stderr, "error: --image can't be attached to an answer; answer the question with -t only")
		return 2, true
	case r.asked && o.task == "":
		fmt.Fprintf(os.Stderr, "error: checkpoint is waiting for an answer to: %s\nanswer it with: --resume %s -t \"<answer>\"\n", src.PendingQuestion.Question, o.resume)
		return 2, true
	case r.asked:
		if r.followUp, err = readTask(o.task, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "error: answer:", err)
			return 2, true
		}
		if strings.TrimSpace(r.followUp) == "" {
			fmt.Fprintf(os.Stderr, "error: the answer is empty; the checkpoint is waiting for an answer to: %s\n", src.PendingQuestion.Question)
			return 2, true
		}
	case taskSet && !r.finished:
		fmt.Fprintln(os.Stderr, "error: --resume and --task are mutually exclusive for an unfinished checkpoint; --task adds a follow-up only to a finished (Submitted or Completed) run")
		return 2, true
	case len(o.imagePaths) > 0 && !r.finished:
		fmt.Fprintln(os.Stderr, "error: --image needs a follow-up task: it attaches images only to a finished checkpoint's --resume -t \"<follow-up>\" message")
		return 2, true
	case r.finished && o.task == "" && len(o.imagePaths) > 0:
		fmt.Fprintln(os.Stderr, "error: --image requires a follow-up task (-t); images are attached to the follow-up message")
		return 2, true
	case r.finished && o.task == "":
		fmt.Fprintf(os.Stderr, "error: checkpoint already finished (%s); a follow-up task is required to continue it: --resume %s -t \"<follow-up>\"\n", src.ExitStatus, o.resume)
		return 2, true
	case r.finished:
		if r.followUp, err = readTask(o.task, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "error: follow-up task:", err)
			return 2, true
		}
	}
	return 0, false
}

// loadConfig loads the config (and its memory settings), registers its
// providers, and loads the lockdown state, task images, OpenTelemetry
// settings, model chain, hooks and permission policy. Resumed runs start
// no MCP servers.
func (r *resumeRun) loadConfig() (int, bool) {
	o, fs := r.o, r.fs
	cfgSrc, cfg, err := loadConfig(o, o.cwd, os.Stderr)
	cfgPath := cfgSrc.Path
	if err == nil {
		err = applyMemoryConfig(&cfg, o.cwd, os.Stderr)
	}
	if err != nil {
		return loadConfigError(err), true
	}
	if o.verbose {
		fmt.Fprintln(os.Stderr, "config:", cfgSrc.Label())
	}
	registerProviders(cfg)
	lock, code := setupResumeLockdown(fs, o.lockdown, &cfg, r.src, os.Stderr)
	if code != 0 {
		return code, true
	}
	r.cfg, r.cfgPath, r.lock = cfg, cfgPath, lock
	if r.taskImages, err = loadTaskImages(o.imagePaths, cfg.MaxImageBytes); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2, true
	}
	announceTaskImages(os.Stderr, r.taskImages, o.verbose)
	if r.otelCfg, err = otelSettings(fs, *o, cfg); err != nil {
		return configFail(err), true
	}
	// Checked after the config load so configured providers resolve.
	if err := checkReasoningEffort(r.modelName, r.src.ReasoningEffort); err != nil {
		return fail(fmt.Errorf("resume: %w", err)), true
	}
	if err := checkResumeProvider(r.src, r.modelName); err != nil {
		return fail(err), true
	}
	// The fallback chain is this invocation's (config or flags), not the
	// checkpoint's; a resumed run starts on the primary model again.
	if r.fallbacks, err = fallbackModels(fs, o.fallbackModels, o.noFallback, cfg); err == nil {
		err = checkFallbacks(r.modelName, r.fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2, true
	}
	if err := model.CheckProviderEnv(r.modelName); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2, true
	}
	if r.hookRunner, err = hooks.New(cfg.Hooks, cfgPath); err != nil {
		return fail(inConfig(cfgPath, err)), true
	}
	if r.policy, err = permissions.New(cfg.Permissions); err != nil {
		return configFail(inConfig(cfgPath, err)), true
	}
	if r.src.MCPEnabled || len(r.src.ExternalTools) > 0 {
		return fail(fmt.Errorf("resume: %w", agent.ErrMCPResume)), true
	}
	if len(cfg.MCPServers) > 0 {
		fmt.Fprintln(os.Stderr, "resume: configured MCP servers are not started; resumed runs use only the built-in tools")
	}
	return 0, false
}

// applyFlags applies this invocation's limit and budget overrides, checks
// the resume contract, then applies the checkpoint's recorded settings
// (resume.Apply makes the checkpoint's win where they must).
func (r *resumeRun) applyFlags() (int, bool) {
	o, fs, cfg := r.o, r.fs, &r.cfg
	applyFlagOverrides(fs, cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "max-format-errors") {
		cfg.MaxFormatErrors = o.maxFormatErrors
	}
	// The output budget may change on resume, e.g. raised after OutputTruncated.
	if flagWasSet(fs, "max-output-tokens") {
		cfg.MaxOutputTokens = o.maxOutputTokens
	}
	if err := checkMaxOutputTokens(r.modelName, cfg.MaxOutputTokens); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2, true
	}
	if code := checkResumeFlags(fs, *o, r.src); code != 0 {
		return code, true
	}
	if err := checkResumeScopedTools(fs, *o, r.src); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return 2, true
	}
	// The checkpoint's recorded settings win; from here on the run is
	// assembled from cfg exactly like a fresh run.
	resume.Apply(r.src, cfg)
	return r.setupFeatures()
}

// setupFeatures sets up web_fetch, web_search and the plugins, then applies
// the snapshot, plan, sandbox, verify and environment settings.
func (r *resumeRun) setupFeatures() (int, bool) {
	o, fs, cfg := r.o, r.fs, &r.cfg
	var err error
	if r.webFetch, err = newWebFetcher(fs, *o, cfg); err != nil {
		return configFail(err), true
	}
	if r.webSearch, err = newWebSearcher(fs, *o, cfg); err != nil {
		return configFail(err), true
	}
	if err := checkPlugins(cfg, o.cwd, os.Stderr, o.verbose, false); err != nil {
		return configFail(err), true
	}
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
		return fail(errors.New("--plan-file requires the plan tool, and this checkpoint was recorded without it")), true
	}
	applySandboxFlags(fs, cfg, *o)
	if err := applyVerifyFlags(fs, cfg, o.verify); err != nil {
		return fail(err), true
	}
	if msg := envConflict(*cfg); msg != "" {
		fmt.Fprintln(os.Stderr, "error: "+msg)
		return 2, true
	}
	if code := checkLSP(*cfg, r.cfgPath, "resume: "); code != 0 {
		return code, true
	}
	model.DefaultRuntime.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}
	return 0, false
}

// setupWorkspace resolves and checks the root against the checkpoint,
// builds the tool output files, file access, additional directories and
// command tools, and validates the output destination and the resume
// contract.
func (r *resumeRun) setupWorkspace() (int, bool) {
	o, fs, cfg := r.o, r.fs, &r.cfg
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return fail(err), true
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("--cwd %s is not a directory", o.cwd)), true
	}
	if root != r.src.Root {
		return fail(fmt.Errorf("resume: --cwd %s does not match the checkpoint's root %s", root, r.src.Root)), true
	}
	r.root = root
	if r.outputFiles, err = toolOutputFiles(fs, *o, *cfg, root); err != nil {
		return fail(err), true
	}
	if r.fileAccess, err = newFileAccess(*cfg, root, r.outputFiles, o.verbose); err != nil {
		return configFail(err), true
	}
	if r.extraDirs, err = additionalDirs(fs, *o, *cfg, root, o.verbose); err != nil {
		return configFail(err), true
	}
	r.fileAccess.AddDirs(r.extraDirs)
	if r.cmdTools, err = commandTools(os.Stderr, *cfg, root); err != nil {
		err = inConfig(r.cfgPath, err)
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		printSetupHint(os.Stderr, err)
		return 2, true
	}
	if cur := agent.AdditionalDirsInfo(r.extraDirs, cfg.ReadOnly); len(cur)+len(r.src.AdditionalDirs) > 0 && fmt.Sprint(cur) != fmt.Sprint(r.src.AdditionalDirs) {
		fmt.Fprintln(os.Stderr, "resume: additional directories differ from the checkpoint's; this run uses the current ones")
	}

	if o.output == "" {
		return fail(errors.New("resume: --output is required to write the continued trajectory")), true
	}
	if sameFile(o.resume, o.output) {
		return fail(errors.New("resume: --output must be distinct from the source checkpoint")), true
	}
	if _, err := os.Lstat(o.output); err == nil {
		return fail(errors.New("resume: --output already exists; choose a new destination")), true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("resume: checking --output: %w", err)), true
	}
	if err := agent.ValidateResume(r.src, *cfg, root); err != nil {
		return fail(fmt.Errorf("resume: %w", err)), true
	}
	if r.src.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}
	warnSnapshotDirs(*cfg, r.extraDirs)
	return 0, false
}

// execute starts the run's resources (LSP, snapshots, the command
// environment), builds the model and the agent, runs the task and maps
// the result to an exit code. The resources are closed when it returns,
// in reverse order.
func (r *resumeRun) execute() int {
	o, fs, cfg := r.o, r.fs, &r.cfg
	lspTools := newLSP(*cfg, r.root)
	if lspTools != nil {
		defer lspTools.Manager.Close()
	}
	snaps, err := openSnapshots(fs, *o, *cfg, r.root, o.output, o.resume)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, closeEnv, err := startEnvironment(ctx, *cfg, r.root, r.extraDirs, o.verbose)
	if err != nil {
		return fail(err)
	}
	defer closeEnv()
	deltas := &textDeltas{}
	m, code, done := r.newModel(deltas)
	if done {
		return code
	}
	a, code, done := r.newAgent(env, m, lspTools, snaps)
	if done {
		return code
	}
	events, warn, setupWarnings, code, done := r.openEvents(a, stop, deltas)
	if done {
		return code
	}
	defer events.close()
	r.prepareAgent(a, events, warn, setupWarnings)
	return r.runAgent(ctx, a, events)
}

// textSink returns the streaming callback for model text: none with
// --no-stream, JSONL deltas with --json, else stdout.
func (r *resumeRun) textSink(deltas *textDeltas) func(string) {
	switch {
	case r.o.noStream:
		return nil
	case r.o.jsonOutput:
		return deltas.write
	}
	return func(s string) { os.Stdout.WriteString(s) }
}

// resolveTransport resolves the OpenAI transport: the checkpoint's recorded
// one, checked against --openai-api, or a fresh resolution for older
// checkpoints with none recorded.
func (r *resumeRun) resolveTransport() (string, int, bool) {
	o, fs, cfg := r.o, r.fs, &r.cfg
	openaiAPI := r.src.OpenAIAPI
	if flagWasSet(fs, "openai-api") {
		if err := checkOpenAIAPI(o.openaiAPI); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return "", 2, true
		}
		want := model.OpenAITransport(r.modelName, o.openaiAPI)
		if openaiAPI != "" && want != openaiAPI {
			return "", fail(fmt.Errorf("resume: --openai-api %q conflicts with the checkpoint's openai_api %q", o.openaiAPI, openaiAPI)), true
		}
		cfg.OpenAIAPI = o.openaiAPI
	}
	if openaiAPI == "" {
		if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return "", 2, true
		}
		openaiAPI = model.OpenAITransport(r.modelName, cfg.OpenAIAPI)
	}
	if openaiAPI != "" && o.verbose {
		fmt.Fprintf(os.Stderr, "openai transport: %s\n", openaiAPI)
	}
	return openaiAPI, 0, false
}

// newModel resolves the recorded transport, then builds the main model
// with the checkpoint's reasoning effort and output budget, and its
// fallbacks, helper models, subagents and workers.
func (r *resumeRun) newModel(deltas *textDeltas) (model.Model, int, bool) {
	o, cfg, src := r.o, &r.cfg, r.src
	onText := r.textSink(deltas)
	openaiAPI, code, done := r.resolveTransport()
	if done {
		return nil, code, true
	}
	r.openaiAPI = openaiAPI
	announceProfile(os.Stderr, o.profile, r.cfgPath, o.verbose)
	announceProvider(os.Stderr, r.modelName, o.verbose)
	m, err := model.NewWith(r.modelName, onText, model.Options{OpenAIAPI: openaiAPI, Handoff: model.HandoffOptions{Dir: r.root, ReadOnly: cfg.ReadOnly}})
	if err != nil {
		code := fail(err)
		printHint(os.Stderr, r.modelName, err.Error())
		return nil, code, true
	}
	if err := model.SetReasoningEffort(m, r.modelName, src.ReasoningEffort); err != nil {
		return nil, fail(fmt.Errorf("resume: %w", err)), true
	}
	if r.helpers, err = newHelpers(*cfg, r.modelName, src.ReasoningEffort); err != nil {
		return nil, fail(err), true
	}
	if r.subagents, err = newSubagents(*cfg, r.modelName, src.ReasoningEffort); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return nil, 2, true
	}
	if r.workers, err = newWorkers(*cfg, r.modelName, src.ReasoningEffort); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return nil, 2, true
	}
	if err := model.SetMaxOutputTokens(m, r.modelName, cfg.MaxOutputTokens); err != nil {
		return nil, fail(fmt.Errorf("resume: %w", err)), true
	}
	warnUnpriced(os.Stderr, r.modelName, cfg.CostLimit)
	if m, err = withFallbacks(m, r.modelName, r.fallbacks, onText, *cfg, src.ReasoningEffort, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return nil, 2, true
	}
	return m, 0, false
}

// newAgent applies --redact-secrets, builds the secret redactor and the
// tool executor from wiring, and creates the agent.
func (r *resumeRun) newAgent(env tools.Environment, m model.Model, lspTools *tools.LSP, snaps agent.Snapshotter) (*agent.Agent, int, bool) {
	o, cfg, src := r.o, &r.cfg, r.src
	providerAPI, providerBaseURL := providerInfo(r.modelName)
	// The current invocation's setting applies; earlier results in the
	// checkpoint are not rewritten.
	if flagWasSet(r.fs, "redact-secrets") {
		cfg.RedactSecrets.Enabled = o.redactSecrets
	}
	redactor, redactInfo := newSecretRedactor(*cfg, os.Environ(), os.Stderr, o.verbose)
	a, err := agent.New(agent.Options{
		Model:           m,
		ModelName:       r.modelName,
		FallbackModels:  r.fallbacks,
		ReasoningEffort: src.ReasoningEffort,
		OpenAIAPI:       r.openaiAPI,
		Profile:         o.profile,
		ProviderAPI:     providerAPI,
		ProviderBaseURL: providerBaseURL,
		// Resumed runs start no MCP servers (see ErrMCPResume in
		// loadConfig), so the MCP and plugin fields of the wiring stay
		// empty.
		Tools: newRunExecutor(*cfg, toolWiring{
			root:        r.root,
			env:         env,
			stream:      jsonStream(o.jsonOutput),
			cmdTools:    r.cmdTools,
			commandLog:  commandLog(o.verbose),
			subagents:   r.subagents,
			workers:     r.workers,
			webFetch:    r.webFetch,
			webSearch:   r.webSearch,
			lsp:         lspTools,
			outputFiles: r.outputFiles,
			fileAccess:  r.fileAccess,
			extraDirs:   r.extraDirs,
			redactor:    redactor,
		}),
		RedactSecrets:  redactInfo,
		Config:         *cfg,
		Hooks:          r.hookRunner,
		Policy:         r.policy,
		Out:            runOutput(o.jsonOutput),
		Err:            os.Stderr,
		Verbose:        o.verbose,
		Streamed:       !o.noStream,
		SkipAgentsMD:   src.SkipAgentsMD,
		ScopedAgentsMD: cfg.ScopedAgentsMD,
		NoCompact:      o.noCompact,
		Resume:         src,
		ResumeSource:   o.resume,
		Worktree:       src.Worktree,
		Snapshots:      snaps,
		FollowUp:       r.followUp,
		TaskImages:     r.taskImages,
		Subagents:      r.subagents,
		Workers:        r.workers,
		Routes:         modelRoutes(*cfg, r.modelName, r.subagents, r.workers),
		Helpers:        r.helpers,
		Lockdown:       r.lock,
	})
	if err != nil {
		return nil, fail(fmt.Errorf("resume: %w", err)), true
	}
	if r.policy != nil && !o.jsonOutput {
		a.PermissionApprover = terminalPermissionApprover
	}
	return a, 0, false
}

// openEvents opens the event sinks (JSONL, the log database,
// OpenTelemetry), records the plugins and checks the tool set. The
// returned warnings are reported once the caller has deferred
// events.close.
func (r *resumeRun) openEvents(a *agent.Agent, stop func(), deltas *textDeltas) (*runEvents, func(string), []string, int, bool) {
	o, fs, cfg := r.o, r.fs, &r.cfg
	var events *runEvents
	warn := func(msg string) { events.warn(msg) }
	db, dbErr := openLogDB(fs, *o, *cfg, r.root, r.src.Task, r.modelName, true, a, warn)
	tr, trWarn := startOTel(r.otelCfg, *cfg, a, a.FollowUp, true, o.verbose)
	a.Plugins = pluginRecords(*cfg, r.root, false)
	if err := validateRunTools(a.Tools); err != nil {
		fmt.Fprintln(os.Stderr, "error: resume:", err)
		return nil, nil, nil, 2, true
	}
	describeScopedTools(os.Stderr, a.Tools.Only)
	events = startEvents(os.Stdout, o.jsonOutput, a, true, stop, db, tr)
	deltas.events = events
	streamToolProgress(a, *o, jsonStream(o.jsonOutput), os.Stderr)
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
// features: tool output files, the checkpoint's skills and plugins, its
// memory, and the checkpoint callback.
func (r *resumeRun) prepareAgent(a *agent.Agent, events *runEvents, warn func(string), setupWarnings []string) {
	o, cfg, src := r.o, &r.cfg, r.src
	for _, w := range setupWarnings {
		warn(w)
	}
	startOutputFiles(r.outputFiles, cfg.ToolOutputFiles.MaxRuns, warn)
	if src.SkillsEnabled {
		loadSkills(a, r.root, pluginSkillSources(*cfg), warn)
	}
	resumePlugins(src, a.Plugins, warn)
	emitResumeMemoryLoaded(events, src, cfg.Memory, r.root)
	if agent.RememberOffered(src) {
		a.Tools.Remember = resumeMemoryWriter(src, cfg.Memory, r.root, r.modelName, a.Tools.Redactor, warn)
		a.Tools.MemoryTools = agent.MemoryToolsOffered(src)
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
}

// runAgent prints the resume notice, runs the task, reports the summary
// and returns the exit code.
func (r *resumeRun) runAgent(ctx context.Context, a *agent.Agent, events *runEvents) int {
	o, src := r.o, r.src
	// The agent records this boundary and clears the active termination
	// before persisting the new starting state.
	prevStatus := src.ExitStatus
	if prevStatus == "" {
		prevStatus = "in progress"
	}
	switch {
	case r.asked:
		fmt.Fprintf(os.Stderr, "answering the question of %s from %s (after %d steps)\n", r.modelName, o.resume, src.Steps)
		fmt.Fprintln(os.Stderr, "note: the full previous conversation is sent to the provider again; the workspace may have changed since the question. Live processes are not restored")
	case r.finished:
		fmt.Fprintf(os.Stderr, "following up %s from %s (previous status %s after %d steps)\n", r.modelName, o.resume, prevStatus, src.Steps)
		fmt.Fprintln(os.Stderr, "note: the full previous conversation is sent to the provider again; the workspace may have changed since the run finished. Live processes are not restored")
	default:
		fmt.Fprintf(os.Stderr, "resuming %s from %s (previous status %s after %d steps)\n", r.modelName, o.resume, prevStatus, src.Steps)
		fmt.Fprintln(os.Stderr, "note: inspect and revalidate the workspace; it may have changed and commands may have produced unrecorded side effects after the last save. Live processes are not restored")
	}

	start := time.Now()
	startLockdown(a, r.lock, os.Stderr, events)
	t := a.Run(ctx, src.Task)
	elapsed := time.Since(start)
	resume := ""
	if !o.jsonOutput {
		resume = resumeCommand(t, o.output, r.root, r.cfg)
	}
	reportSummary(os.Stderr, r.modelName, t, elapsed, resume)
	if events.finish(t, elapsed) != nil {
		return 1
	}
	return exitCode(t, o.jsonOutput)
}
