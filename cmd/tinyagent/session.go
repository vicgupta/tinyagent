package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

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
	o    *options
	fs   *flag.FlagSet
	cfg  config.AgentConfig
	root string

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
	setupLockdown  func(cfg *config.AgentConfig) (*lockdown.State, error)
	beforeWebFetch func(cfg *config.AgentConfig)
	beforeEnvCheck func(cfg *config.AgentConfig)
	mcpServers     func(cfg config.AgentConfig) ([]config.MCPServer, error)
	startLockdown  func()
}

// setup sets the session up the way run sets up a headless run in s.root,
// from the same config and flags. Errors carry the message (and any hint)
// a headless run prints.
func (s *session) setup(ctx context.Context, m sessionMode) (err error) {
	// The project config is the session's: tinyagent.yaml in its cwd.
	o, fs, root := s.o, s.fs, s.root
	cfgSrc, cfg, err := loadConfig(o, root, os.Stderr)
	cfgPath := cfgSrc.Path
	if err == nil {
		err = applyMemoryConfig(&cfg, o.cwd, os.Stderr)
	}
	if err != nil {
		return err
	}
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
	applySandboxFlags(fs, &cfg, *o)
	if err := applyVerifyFlags(fs, &cfg, o.verify); err != nil {
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
		setWorkers(&cfg, o.workers)
	}
	if err := checkWorkers(cfg, false, root); err != nil {
		return err
	}
	if flagWasSet(fs, "web-fetch") {
		cfg.WebFetch.Enabled = o.webFetch
	}
	m.beforeWebFetch(&cfg)
	if s.webFetch, err = newWebFetcher(fs, *o, &cfg); err != nil {
		return err
	}
	if err := checkPlugins(&cfg, os.Stderr, o.verbose, true); err != nil {
		return err
	}
	if flagWasSet(fs, "lsp") {
		cfg.LSP.Enabled = o.lsp
	}
	if flagWasSet(fs, "scoped-agents-md") {
		cfg.ScopedAgentsMD = o.scopedAgentsMD
	}
	if err := lspError(cfg, cfgPath); err != nil {
		return err
	}
	if on, set, _ := skillsFlag(fs, *o); set {
		cfg.Skills = on
	}
	if flagWasSet(fs, "memory") {
		cfg.Memory.Enabled = o.memory.enabled
	}
	m.beforeEnvCheck(&cfg)
	if msg := envConflict(cfg); msg != "" {
		return errors.New(msg)
	}
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
		return withHint(s.modelName, fmt.Sprintf("%s is not set; set it in the agent's env in the editor's agent settings", env), "HTTP 401: ")
	}
	if s.fallbacks, err = fallbackModels(fs, o.fallbackModels, o.noFallback, cfg); err == nil {
		err = checkFallbacks(s.modelName, s.fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		return err
	}
	if s.subagents, err = newSubagents(cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if s.workers, err = newWorkers(cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if s.helpers, err = newHelpers(cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	model.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}
	if s.outputFiles, err = toolOutputFiles(fs, *o, cfg, root); err != nil {
		return err
	}
	if s.fileAccess, err = newFileAccess(cfg, root, s.outputFiles, o.verbose); err != nil {
		return err
	}
	if s.extraDirs, err = additionalDirs(fs, *o, cfg, root, o.verbose); err != nil {
		return err
	}
	s.fileAccess.AddDirs(s.extraDirs)
	if cfg.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}
	if s.cmdTools, err = commandTools(os.Stderr, cfg, root); err != nil {
		return inConfig(cfgPath, err)
	}
	mcpDefs, err := m.mcpServers(cfg)
	if err != nil {
		return err
	}
	env, closeEnv, err := startEnvironment(ctx, cfg, root, s.extraDirs, o.verbose)
	if err != nil {
		return err
	}
	s.env, s.closeEnv = env, closeEnv
	if s.mcp, err = mcptools.Start(ctx, root, mcpDefs, os.Stderr); err != nil {
		return err
	}
	if s.extSpecs, s.extCall, err = externalTools(s.mcp, startWasmTools(ctx, cfg, root, s.fileAccess, s.lockdown, os.Stderr)); err != nil {
		return err
	}
	if err := checkCommandToolNames(s.cmdTools, s.extSpecs); err != nil {
		return err
	}
	m.startLockdown()

	var onText func(string)
	if !o.noStream {
		onText = s.streamed
	}
	announceProfile(os.Stderr, o.profile, cfgPath, o.verbose)
	announceProvider(os.Stderr, s.modelName, o.verbose)
	if s.model, err = model.NewWith(s.modelName, onText, model.Options{OpenAIAPI: s.openaiAPI, Handoff: model.HandoffOptions{Dir: root, ReadOnly: cfg.ReadOnly}}); err != nil {
		return withHint(s.modelName, err.Error(), err.Error())
	}
	if err := model.SetReasoningEffort(s.model, s.modelName, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := model.SetMaxOutputTokens(s.model, s.modelName, cfg.MaxOutputTokens); err != nil {
		return err
	}
	warnUnpriced(os.Stderr, s.modelName, cfg.CostLimit)
	if s.model, err = withFallbacks(s.model, s.modelName, s.fallbacks, onText, cfg, cfg.ReasoningEffort, os.Stderr); err != nil {
		return err
	}
	s.providerAPI, s.providerBaseURL = providerInfo(s.modelName)
	if flagWasSet(fs, "redact-secrets") {
		cfg.RedactSecrets.Enabled = o.redactSecrets
	}
	s.redactor, s.redactInfo = newSecretRedactor(cfg, os.Environ(), os.Stderr, o.verbose)
	s.cfg = cfg
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
	return agent.New(agent.Options{
		Model:           s.model,
		ModelName:       s.modelName,
		FallbackModels:  s.fallbacks,
		ReasoningEffort: cfg.ReasoningEffort,
		OpenAIAPI:       s.openaiAPI,
		Profile:         o.profile,
		ProviderAPI:     s.providerAPI,
		ProviderBaseURL: s.providerBaseURL,
		Tools: &tools.Executor{
			Root:               s.root,
			ReadOnly:           cfg.ReadOnly,
			Env:                s.env,
			BashTimeout:        time.Duration(cfg.BashTimeoutSec) * time.Second,
			MaxOutputChars:     cfg.MaxOutputChars,
			MaxProcesses:       cfg.MaxBackgroundProcesses,
			ExternalSpecs:      s.extSpecs,
			ExternalCall:       s.extCall,
			Plan:               cfg.Plan.Enabled,
			Images:             cfg.Images.Enabled,
			ApplyPatch:         cfg.ApplyPatch,
			Delegate:           s.subagents != nil,
			TaskTypes:          taskTypes(s.cfg, s.subagents, s.workers),
			Workers:            s.workers != nil,
			AskUser:            cfg.AskUser.Enabled, // always off in ACP sessions
			WebFetch:           s.webFetch,
			LSP:                newLSP(cfg, s.root), // a turn's end stops them
			OutputFiles:        s.outputFiles,
			FileAccess:         s.fileAccess,
			ExtraDirs:          s.extraDirs,
			MaxImageBytes:      cfg.MaxImageBytes,
			ExternalCallImages: s.mcp.CallImages(cfg.MaxImageBytes),
			Redactor:           s.redactor,
			CommandTools:       s.cmdTools,
			CommandLog:         commandLog(o.verbose),
		},
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
