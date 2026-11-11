package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/telemetry"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// session is a multi-turn conversation held in one process: a working
// directory with the settings, environment, MCP servers and model of a
// headless run there, and the conversation so far. Each turn is one
// Agent.Run; later ones continue the previous turn's trajectory like a
// --resume -t follow-up. ACP sessions (acpSession) are built on it.
type session struct {
	o       *options
	fs      *flag.FlagSet
	cfg     config.AgentConfig
	cfgSrc  config.Source
	cfgPath string
	root    string

	modelName, openaiAPI         string
	providerAPI, providerBaseURL string
	fallbacks                    []string
	model                        model.Model
	subagents                    *agent.Subagents
	workers                      *agent.Workers
	helpers                      map[string]model.Model
	hooks                        *hooks.Runner
	policy                       *permissions.Policy
	env                          tools.Environment
	closeEnv                     func()
	mcp                          *mcptools.Registry
	webFetch                     *tools.WebFetcher
	webSearch                    *tools.WebSearcher
	outputFiles                  *tools.OutputFiles
	fileAccess                   *tools.FileAccess
	extraDirs                    []tools.ExtraDir
	redactor                     *eventlog.Redactor
	redactInfo                   *trajectory.RedactSecretsInfo
	otel                         *telemetry.Settings
	lockdown                     *lockdown.State
	extSpecs                     []model.ToolSpec // MCP plus plugin wasm tools
	extCall                      func(ctx context.Context, name, raw string) (string, bool, bool)
	cmdTools                     []*tools.CommandTool
	remember                     *memory.Writer
	traj                         *trajectory.Trajectory
	turn                         int
	textMu                       sync.Mutex
	text                         func(string) // the running turn's streamed text
}

// sessionMode holds what a front end (ACP) changes in session setup.
type sessionMode struct {
	// interactive is a chat session on a terminal: skills default on there
	// (#359); ACP sessions are driven by an editor and default off.
	interactive    bool
	setupLockdown  func(cfg *config.AgentConfig) (*lockdown.State, error)
	beforeWebFetch func(cfg *config.AgentConfig)
	beforeEnvCheck func(cfg *config.AgentConfig)
	mcpServers     func(cfg config.AgentConfig) ([]config.MCPServer, error)
	startLockdown  func()
}

// setup sets the session up the way run sets up a headless run in s.root,
// from the same config and flags. Errors carry the message (and any hint)
// a headless run prints. The phases live in the session's setup helpers
// (issue #416).
func (s *session) setup(ctx context.Context, m sessionMode) (err error) {
	if err := s.loadSessionConfig(m); err != nil {
		return err
	}
	if err := s.applySessionFlags(m); err != nil {
		return err
	}
	if err := s.resolveSessionModel(); err != nil {
		return err
	}
	if err := s.setupSessionWorkspace(ctx, m); err != nil {
		return err
	}
	return s.buildSessionModel()
}

// loadSessionConfig loads the project config (and its memory settings) and
// builds the lockdown state, hooks runner, permission policy and
// OpenTelemetry settings.
func (s *session) loadSessionConfig(m sessionMode) error {
	// The project config is the session's: .tinyagent/tinyagent.yaml in its cwd.
	o, fs, root := s.o, s.fs, s.root
	cfgSrc, cfg, err := loadConfig(o, root, os.Stderr)
	cfgPath := cfgSrc.Path
	if err == nil {
		err = applyMemoryConfig(&cfg, o.cwd, os.Stderr)
	}
	if err != nil {
		return err
	}
	s.cfg, s.cfgSrc, s.cfgPath = cfg, cfgSrc, cfgPath
	registerProviders(cfg)
	if s.lockdown, err = m.setupLockdown(&cfg); err != nil {
		return err
	}
	if s.hooks, err = hooks.New(cfg.Hooks, cfgPath); err != nil {
		return err
	}
	if s.policy, err = permissions.New(cfg.Permissions); err != nil {
		return err
	}
	if s.otel, err = otelSettings(fs, *o, cfg); err != nil {
		return err
	}
	return nil
}

// applySessionFlags applies the command-line overrides of limits,
// read-only, tool scope, sandbox, verify, plan, images, apply_patch,
// subagents, workers, web tools, plugins, LSP, scoped AGENTS.md, skills,
// memory and the environment checks to the config.
func (s *session) applySessionFlags(m sessionMode) error {
	o, fs, cfg := s.o, s.fs, &s.cfg
	root := s.root
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
		return err
	}
	describeScopedTools(os.Stderr, cfg.Tools)
	applySandboxFlags(fs, cfg, *o)
	if err := applyVerifyFlags(fs, cfg, o.verify); err != nil {
		return err
	}
	if flagWasSet(fs, "plan") {
		cfg.Plan.Enabled = o.plan
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
	if flagWasSet(fs, "workers") {
		setWorkers(cfg, o.workers)
	}
	if err := checkWorkers(*cfg, false, root); err != nil {
		return err
	}
	m.beforeWebFetch(cfg)
	var err error
	if s.webFetch, err = newWebFetcher(fs, *o, cfg); err != nil {
		return err
	}
	if s.webSearch, err = newWebSearcher(fs, *o, cfg); err != nil {
		return err
	}
	if err := checkPlugins(cfg, o.cwd, os.Stderr, o.verbose, true); err != nil {
		return err
	}
	if flagWasSet(fs, "lsp") {
		cfg.LSP.Enabled = o.lsp
	}
	if flagWasSet(fs, "scoped-agents-md") {
		cfg.ScopedAgentsMD = o.scopedAgentsMD
	}
	if err := lspError(*cfg, s.cfgPath); err != nil {
		return err
	}
	skillsDec := applySkillsDefault(fs, *o, cfg, s.cfgSrc, skillsMode{
		Interactive: m.interactive,
		Hardened:    hardenedReason(false, false, cfg.UntrustedContent.Enabled, cfg.ReadOnly),
	})
	if o.verbose {
		fmt.Fprintln(os.Stderr, "skills:", skillsDec)
	}
	if flagWasSet(fs, "memory") {
		cfg.Memory.Enabled = o.memory.enabled
	}
	m.beforeEnvCheck(cfg)
	if msg := envConflict(*cfg); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// resolveSessionModel resolves the main model and its reasoning effort,
// output budget and OpenAI transport, checks the provider environment,
// and builds the fallbacks, subagents, workers and helper models.
func (s *session) resolveSessionModel() error {
	o, fs, cfg := s.o, s.fs, &s.cfg
	s.modelName = resolveModel(fs, o.modelName, cfg.Model)
	if flagWasSet(fs, "reasoning-effort") {
		cfg.ReasoningEffort = o.reasoningEffort
	}
	if err := checkReasoningEffort(s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := checkMaxOutputTokens(s.modelName, cfg.MaxOutputTokens); err != nil {
		return err
	}
	if flagWasSet(fs, "openai-api") {
		cfg.OpenAIAPI = o.openaiAPI
	}
	if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
		return err
	}
	s.openaiAPI = model.OpenAITransport(s.modelName, cfg.OpenAIAPI)
	if err := model.CheckProviderEnv(s.modelName); err != nil {
		return withHint(s.modelName, err.Error(), err.Error())
	}
	if env := model.MissingKeyEnv(s.modelName); env != "" {
		return withHint(s.modelName, fmt.Sprintf("%s is not set; export it, or set it under env: in ./%s (for ACP, the agent's env in the editor's agent settings also works)", env, config.ProjectFile), "HTTP 401: ")
	}
	var err error
	if s.fallbacks, err = fallbackModels(fs, o.fallbackModels, o.noFallback, *cfg); err == nil {
		err = checkFallbacks(s.modelName, s.fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		return err
	}
	if s.subagents, err = newSubagents(*cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if s.workers, err = newWorkers(*cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if s.helpers, err = newHelpers(*cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	model.DefaultRuntime.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}
	return nil
}

// setupSessionWorkspace builds the tool output files, file access,
// additional directories and command tools, then starts the command
// environment and the MCP and plugin tools.
func (s *session) setupSessionWorkspace(ctx context.Context, m sessionMode) error {
	o, fs, cfg := s.o, s.fs, &s.cfg
	root := s.root
	var err error
	if s.outputFiles, err = toolOutputFiles(fs, *o, *cfg, root); err != nil {
		return err
	}
	if s.fileAccess, err = newFileAccess(*cfg, root, s.outputFiles, o.verbose); err != nil {
		return err
	}
	if s.extraDirs, err = additionalDirs(fs, *o, *cfg, root, o.verbose); err != nil {
		return err
	}
	s.fileAccess.AddDirs(s.extraDirs)
	if cfg.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}
	if s.cmdTools, err = commandTools(os.Stderr, *cfg, root); err != nil {
		return inConfig(s.cfgPath, err)
	}
	mcpDefs, err := m.mcpServers(*cfg)
	if err != nil {
		return err
	}
	env, closeEnv, err := startEnvironment(ctx, *cfg, root, s.extraDirs, o.verbose)
	if err != nil {
		return err
	}
	s.env, s.closeEnv = env, closeEnv
	if s.mcp, err = mcptools.Start(ctx, root, mcpDefs, os.Stderr); err != nil {
		return err
	}
	if s.extSpecs, s.extCall, err = externalTools(s.mcp, startWasmTools(ctx, *cfg, root, s.fileAccess, s.lockdown, os.Stderr)); err != nil {
		return err
	}
	if err := checkCommandToolNames(s.cmdTools, s.extSpecs); err != nil {
		return err
	}
	m.startLockdown()
	return nil
}

// buildSessionModel builds the main model with its reasoning effort,
// output budget and fallbacks, and the secret redactor.
func (s *session) buildSessionModel() error {
	o, fs, cfg := s.o, s.fs, &s.cfg
	var onText func(string)
	if !o.noStream {
		onText = s.streamed
	}
	announceProfile(os.Stderr, o.profile, s.cfgPath, o.verbose)
	announceProvider(os.Stderr, s.modelName, o.verbose)
	var err error
	if s.model, err = model.NewWith(s.modelName, onText, model.Options{OpenAIAPI: s.openaiAPI, Handoff: model.HandoffOptions{Dir: s.root, ReadOnly: cfg.ReadOnly}}); err != nil {
		return withHint(s.modelName, err.Error(), err.Error())
	}
	if err := model.SetReasoningEffort(s.model, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := model.SetMaxOutputTokens(s.model, s.modelName, cfg.MaxOutputTokens); err != nil {
		return err
	}
	warnUnpriced(os.Stderr, s.modelName, cfg.CostLimit)
	if s.model, err = withFallbacks(s.model, s.modelName, s.fallbacks, onText, *cfg, cfg.ReasoningEffort, os.Stderr); err != nil {
		return err
	}
	s.providerAPI, s.providerBaseURL = providerInfo(s.modelName)
	if flagWasSet(fs, "redact-secrets") {
		cfg.RedactSecrets.Enabled = o.redactSecrets
	}
	s.redactor, s.redactInfo = newSecretRedactor(*cfg, os.Environ(), os.Stderr, o.verbose)
	return nil
}

// turnConfig is the config for the turn after prev: limits apply to each
// turn, so they are offset by what the conversation used so far.
func (s *session) turnConfig(prev *trajectory.Trajectory) config.AgentConfig {
	cfg := s.cfg
	if prev != nil {
		if cfg.StepLimit > 0 {
			cfg.StepLimit += prev.Steps
		}
		if cfg.CostLimit > 0 {
			cfg.CostLimit += prev.Cost
		}
		if cfg.TokenLimit > 0 {
			cfg.TokenLimit += prev.InputTokens + prev.OutputTokens
		}
	}
	return cfg
}

// turnResume is what a turn continues: the previous turn's trajectory (nil
// for the first turn), its audit source, the follow-up message, and whether
// prev is this process's own earlier turn (agent.Options.Continue).
type turnResume struct {
	prev             *trajectory.Trajectory
	source, followUp string
	cont             bool
}

// turnAgent builds the Agent for one turn with cfg and images, continuing r.
func (s *session) turnAgent(cfg config.AgentConfig, images []model.Image, r turnResume) (*agent.Agent, error) {
	o := s.o
	a, err := agent.New(agent.Options{
		Model:           s.model,
		ModelName:       s.modelName,
		FallbackModels:  s.fallbacks,
		ReasoningEffort: cfg.ReasoningEffort,
		OpenAIAPI:       s.openaiAPI,
		Profile:         o.profile,
		ProviderAPI:     s.providerAPI,
		ProviderBaseURL: s.providerBaseURL,
		// AskUser is always off in ACP sessions; a turn's end stops its LSP
		// servers, so each turn starts its own.
		Tools: newRunExecutor(cfg, toolWiring{
			root:        s.root,
			env:         s.env,
			cmdTools:    s.cmdTools,
			commandLog:  commandLog(o.verbose),
			extSpecs:    s.extSpecs,
			extCall:     s.extCall,
			mcp:         s.mcp,
			subagents:   s.subagents,
			workers:     s.workers,
			webFetch:    s.webFetch,
			webSearch:   s.webSearch,
			lsp:         newLSP(cfg, s.root),
			outputFiles: s.outputFiles,
			fileAccess:  s.fileAccess,
			extraDirs:   s.extraDirs,
			redactor:    s.redactor,
		}),
		RedactSecrets:  s.redactInfo,
		Config:         cfg,
		Hooks:          s.hooks,
		Policy:         s.policy,
		Out:            os.Stderr,
		Err:            os.Stderr,
		Verbose:        o.verbose,
		Streamed:       !o.noStream,
		SkipAgentsMD:   o.noAgentsMD,
		ScopedAgentsMD: cfg.ScopedAgentsMD && !o.noAgentsMD,
		NoCompact:      o.noCompact,
		Subagents:      s.subagents,
		Workers:        s.workers,
		Helpers:        s.helpers,
		TaskImages:     images,
		Lockdown:       s.lockdown,
		Resume:         r.prev,
		ResumeSource:   r.source,
		FollowUp:       r.followUp,
		Continue:       r.cont,
	})
	if err != nil {
		return nil, err
	}
	if err := validateRunTools(a.Tools); err != nil {
		return nil, err
	}
	return a, nil
}

// streamed forwards the model's streamed text to the running turn.
func (s *session) streamed(text string) {
	s.textMu.Lock()
	f := s.text
	s.textMu.Unlock()
	if f != nil {
		f(text)
	}
}

func (s *session) setText(f func(string)) {
	s.textMu.Lock()
	s.text = f
	s.textMu.Unlock()
}

// Close stops the session's MCP servers and releases its environment.
// Background processes and language servers already stopped with the last
// turn.
func (s *session) Close() {
	if s.mcp != nil {
		s.mcp.Close()
	}
	s.closeEnv()
}
