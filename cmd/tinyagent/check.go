package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/routing"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// runCheck implements --check: it loads the config and resolves the model,
// provider, fallbacks, cwd and limits the way a run would, prints where
// each came from, and exits without any model or network request and
// without writing anything. Any setup error a run would refuse exits 2 with
// the same message. Key values are never printed, only whether the
// variable is set.
func runCheck(o *options, fs *flag.FlagSet, w io.Writer) int {
	// With --attempts, --worktree=<name> names the attempts' worktrees,
	// which --check only looks up.
	if o.task != "" || o.resume != "" || o.rewind != "" || (o.worktree.on && !attemptsActive(fs)) {
		fmt.Fprintln(os.Stderr, "error: --check makes no model request; it can't be combined with --task, --resume, --rewind or --worktree")
		return 2
	}
	if flagWasSet(fs, "to", "dry-run") {
		fmt.Fprintln(os.Stderr, "error: --to and --dry-run apply only to --rewind")
		return 2
	}
	sum := newCheckSummary()
	r, err := resolveCheck(o, fs, sum)
	if err != nil {
		fmt.Fprintln(os.Stderr, stderrStyle.Red("error:"), err)
		sum.Error = &checkError{Message: err.Error()}
		var hint checkHint
		if errors.As(err, &hint) {
			printHint(os.Stderr, hint.spec, hint.msg)
			sum.Error.Hint = model.Hint(hint.spec, hint.msg)
		} else {
			printSetupHint(os.Stderr, err)
			sum.Error.Hint = setupHint(err)
		}
		if o.jsonOutput {
			sum.writeJSON(w)
		}
		return 2
	}
	sum.OK = true
	if sum.Commands.Warning != "" {
		fmt.Fprintln(os.Stderr, stderrStyle.Yellow("warning:"), sum.Commands.Warning)
	}
	if o.jsonOutput {
		sum.writeJSON(w)
		return 0
	}
	sum.writeTable(w)
	for _, l := range r {
		fmt.Fprintf(w, "%s %s\n", styleFor(w).Bold(fmt.Sprintf("%-10s", l[0]+":")), l[1])
	}
	fmt.Fprintln(w, styleFor(w).Green("ok"))
	return 0
}

// checkHint marks an error that has a setup hint: the hint is looked up for
// spec as if a request had failed with msg.
type checkHint struct {
	spec, msg string
	err       error
}

func (h checkHint) Error() string { return h.err.Error() }

// checkReport accumulates a --check run's state between its phases
// (issue #416): the resolved config, model, workspace, the report lines
// and the summary fields filled as it goes, so a failed check still
// reports what it resolved before the error. The phases run in the order
// resolveCheck has always used.
type checkReport struct {
	o       *options
	fs      *flag.FlagSet
	cfg     config.AgentConfig
	cfgSrc  config.Source
	cfgPath string
	sum     *checkSummary
	policy  *permissions.Policy

	spec       string
	specSource string
	fallbacks  []string
	m          model.Model
	root       string
	cmdTools   []*tools.CommandTool
	lines      [][2]string
}

func (rp *checkReport) add(k, v string) { rp.lines = append(rp.lines, [2]string{k, v}) }

// resolveCheck returns the --check report lines, or the first error a run
// would stop on before its first request. It fills sum as it goes, so a
// failed check still reports what it resolved before the error. The
// phases live in checkReport's methods.
func resolveCheck(o *options, fs *flag.FlagSet, sum *checkSummary) ([][2]string, error) {
	rp, err := newCheckReport(o, fs, sum)
	if err != nil {
		return nil, err
	}
	if err := rp.resolveModel(); err != nil {
		return nil, err
	}
	if err := rp.resolveWorkspace(); err != nil {
		return nil, err
	}
	if err := rp.reportLines(); err != nil {
		return nil, err
	}
	return rp.lines, nil
}

// newCheckReport loads the config the way a run would, builds the hooks
// runner and the permission policy, and applies the limit, tool-scope and
// output-budget overrides --check reports on.
func newCheckReport(o *options, fs *flag.FlagSet, sum *checkSummary) (*checkReport, error) {
	cfgSrc, cfg, err := loadConfig(o, o.cwd, io.Discard)
	cfgPath := cfgSrc.Path
	if err == nil {
		err = applyMemoryConfig(&cfg, o.cwd, io.Discard)
	}
	if err != nil {
		return nil, err
	}
	registerProviders(cfg)
	if _, err := hooks.New(cfg.Hooks, cfgPath); err != nil {
		return nil, inConfig(cfgPath, err)
	}
	policy, err := permissions.New(cfg.Permissions)
	if err != nil {
		return nil, inConfig(cfgPath, err)
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if _, err := resolveScopedTools(fs, *o, &cfg); err != nil {
		return nil, err
	}
	if flagWasSet(fs, "max-output-tokens") {
		cfg.MaxOutputTokens = o.maxOutputTokens
	}
	return &checkReport{o: o, fs: fs, cfg: cfg, cfgSrc: cfgSrc, cfgPath: cfgPath, sum: sum, policy: policy}, nil
}

// resolveModel resolves the model spec and where it came from, checks the
// reasoning effort, output budget, OpenAI transport, provider environment
// and fallbacks, and builds the client (which makes no request) to report
// the provider's credentials.
func (rp *checkReport) resolveModel() error {
	o, fs, cfg := rp.o, rp.fs, &rp.cfg
	spec := resolveModel(fs, o.modelName, cfg.Model)
	specSource := modelSource(fs, cfg.Model)
	if attemptsActive(fs) && len(o.attempts.models) > 0 {
		// --attempt-model replaces -m: report and check attempt 1's model
		// here; checkAttempts checks every attempt's.
		spec, specSource = o.attempts.models[0], "attempt 1, from --attempt-model"
	}
	if flagWasSet(fs, "reasoning-effort") {
		cfg.ReasoningEffort = o.reasoningEffort
	}
	if err := checkReasoningEffort(spec, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := checkMaxOutputTokens(spec, cfg.MaxOutputTokens); err != nil {
		return err
	}
	if flagWasSet(fs, "openai-api") {
		cfg.OpenAIAPI = o.openaiAPI
	}
	if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
		return err
	}
	rp.spec, rp.specSource = spec, specSource
	rp.sum.Profile = o.profile
	rp.sum.Model = checkModel{Spec: spec, Source: strings.TrimPrefix(specSource, "from ")}
	rp.sum.setProvider(spec)
	rp.sum.Limits = checkLimits{Steps: cfg.StepLimit, CostUSD: cfg.CostLimit, Tokens: cfg.TokenLimit, TimeSec: cfg.WallTimeSec}
	if err := model.CheckProviderEnv(spec); err != nil {
		return err
	}
	fallbacks, err := fallbackModels(fs, o.fallbackModels, o.noFallback, *cfg)
	if err == nil {
		err = checkFallbacks(spec, fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		return err
	}
	rp.fallbacks = fallbacks
	// Building the client makes no request; it reports an unknown provider,
	// a missing model name or a missing Codex login, as a run would.
	m, err := model.NewWith(spec, nil, model.Options{OpenAIAPI: model.OpenAITransport(spec, cfg.OpenAIAPI)})
	if err != nil {
		return checkHint{spec: spec, msg: err.Error(), err: err}
	}
	if env := model.MissingKeyEnv(spec); env != "" {
		return checkHint{spec: spec, msg: "HTTP 401: ", err: fmt.Errorf("%s is not set", env)}
	}
	rp.m = m
	return nil
}

// resolveWorkspace resolves the working directory and loads the command
// tools the way a run would.
func (rp *checkReport) resolveWorkspace() error {
	o, cfg := rp.o, &rp.cfg
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return fmt.Errorf("--cwd %s is not a directory", o.cwd)
	}
	cmdTools, err := tools.NewCommandTools(cfg.CommandTools, root, cfg.BashTimeoutSec)
	if err != nil {
		if rp.cfgPath == "" {
			return fmt.Errorf("config: %w", err)
		}
		return inConfig(rp.cfgPath, err)
	}
	rp.root, rp.cmdTools = root, cmdTools
	return nil
}

// reportLines builds the report lines section by section, filling the
// summary as it goes.
func (rp *checkReport) reportLines() error {
	rp.headerLines()
	if err := rp.routeLines(); err != nil {
		return err
	}
	if err := rp.settingLines(); err != nil {
		return err
	}
	return rp.postureLines()
}

// headerLines reports the config, profile, env, model, provider and
// fallback lines.
func (rp *checkReport) headerLines() {
	o, fs := rp.o, rp.fs
	rp.add("config", rp.cfgSrc.Label())
	rp.sum.setConfigLine(rp.cfgSrc.Label())
	if o.profile != "" {
		rp.add("profile", o.profile)
	}
	if env := describeConfigEnv(rp.cfg.Env); env != "" {
		rp.add("env", env) // names only, never values (#410)
	}
	rp.add("model", rp.spec+" ("+rp.specSource+")")
	rp.add("provider", providerStatus(rp.spec, rp.m))
	switch {
	case len(rp.fallbacks) == 0 && o.noFallback:
		rp.add("fallback", "none (--no-fallback)")
	case len(rp.fallbacks) == 0:
		rp.add("fallback", "none")
	case flagWasSet(fs, "fallback-model"):
		rp.add("fallback", strings.Join(rp.fallbacks, ", ")+" (from --fallback-model)")
	default:
		rp.add("fallback", strings.Join(rp.fallbacks, ", ")+" (from config)")
	}
}

// routeLines reports the model routing (auxiliary tasks routed away from
// the main model, and the delegate and worker routes) plus the cwd,
// limits and MCP lines.
func (rp *checkReport) routeLines() error {
	o, fs, cfg := rp.o, rp.fs, &rp.cfg
	if flagWasSet(fs, "subagents") {
		cfg.Subagents.Enabled = o.subagents
	}
	if flagWasSet(fs, "workers") {
		setWorkers(cfg, o.workers)
	}
	for _, r := range checkRoutes(*cfg, rp.spec) {
		if r.Model != rp.spec || len(r.Fallbacks) > 0 {
			if _, err := routedModel(*cfg, r, rp.spec, cfg.ReasoningEffort); err != nil {
				return err
			}
		}
		line := r.Model + " (" + r.Role + ", from " + r.Source + ")"
		if len(r.Fallbacks) > 0 {
			line += ", fallback " + strings.Join(r.Fallbacks, " -> ")
		}
		rp.add(r.Task, line)
	}
	if cfg.Subagents.Enabled || workersEnabled(*cfg) {
		if _, err := childRoutes(*cfg, rp.spec, routing.TaskDelegate, cfg.ReasoningEffort); err != nil {
			return err
		}
		for i, mr := range cfg.Models.Delegated.Routes {
			var when []string
			if len(mr.Match) > 0 {
				when = append(when, "match "+strings.Join(mr.Match, ", "))
			}
			if mr.Type != "" {
				when = append(when, "type "+mr.Type)
			}
			rp.add("route", fmt.Sprintf("%d: %s -> %s", i+1, strings.Join(when, " or "), mr.Model))
		}
	} else if cfg.Models.Flags.NoRouting {
		rp.add("route", "off (--no-routing)")
	}
	cwdSource := "default"
	if flagWasSet(fs, "cwd") {
		cwdSource = "from --cwd"
	}
	rp.add("cwd", rp.root+" ("+cwdSource+")")
	rp.sum.Cwd = rp.root
	rp.add("limits", strings.Join([]string{
		"steps " + limitText(fs, "step-limit", cfg.StepLimit > 0, fmt.Sprint(cfg.StepLimit)),
		"cost " + limitText(fs, "cost-limit", cfg.CostLimit > 0, formatCost(cfg.CostLimit)),
		"tokens " + limitText(fs, "token-limit", cfg.TokenLimit > 0, fmt.Sprint(cfg.TokenLimit)),
		"time " + limitText(fs, "time-limit", cfg.WallTimeSec > 0, fmt.Sprintf("%ds", cfg.WallTimeSec)),
	}, ", "))
	for _, s := range cfg.MCPServers {
		rp.add("mcp", fmt.Sprintf("%s: %d tools, %d resource patterns", s.Alias, len(s.Tools), len(s.Resources)))
	}
	return nil
}

// settingLines reports the plugins, ask_user, command tools, tool scope
// and sandbox lines.
func (rp *checkReport) settingLines() error {
	o, fs, cfg := rp.o, rp.fs, &rp.cfg
	pl, err := checkPluginsLine(*cfg, o.cwd, os.Stderr)
	if err != nil {
		return err
	}
	if pl != "" {
		rp.add("plugins", pl)
	}
	if flagWasSet(fs, "ask-user") {
		cfg.AskUser.Enabled = o.askUser
	}
	switch {
	case cfg.AskUser.Enabled && flagWasSet(fs, "ask-user"):
		rp.add("ask_user", fmt.Sprintf("on, at most %d questions per run (from --ask-user)", cfg.AskUser.MaxQuestions))
	case cfg.AskUser.Enabled:
		rp.add("ask_user", fmt.Sprintf("on, at most %d questions per run (from config)", cfg.AskUser.MaxQuestions))
	case flagWasSet(fs, "ask-user"):
		rp.add("ask_user", "off (from --ask-user)")
	}
	if n := len(rp.cmdTools); n > 0 {
		names := make([]string, n)
		for i, c := range rp.cmdTools {
			names[i] = c.Name
		}
		rp.add("tools", fmt.Sprintf("%d command tools (%s)", n, strings.Join(names, ", ")))
	}
	if len(cfg.Tools) > 0 {
		rp.add("tool_scope", strings.Join(cfg.Tools, ", "))
	}
	if sb, err := checkSandbox(fs, *o, *cfg); err != nil {
		return err
	} else if sb != "" {
		rp.add("sandbox", sb)
		rp.sum.Sandbox = checkSandboxInfo{Enabled: true, Backend: sandboxBackendName(*cfg, fs, *o), Available: !strings.HasPrefix(sb, "unavailable"), Detail: sb}
	}
	return nil
}

// postureLines reports the command and web posture, memory, lockdown,
// skills, attempts and permissions lines.
func (rp *checkReport) postureLines() error {
	o, fs, cfg := rp.o, rp.fs, &rp.cfg
	cmdPosture, webPosture, err := checkPosture(fs, *o, *cfg)
	if err != nil {
		return err
	}
	rp.add("commands", commandsLine(cmdPosture))
	rp.add("web", webLine(webPosture))
	rp.sum.Commands, rp.sum.WebTools = cmdPosture, webPosture
	if mem := checkMemory(fs, o, cfg.Memory, rp.root); mem != "" {
		rp.add("memory", mem)
	}
	lock, err := checkLockdown(fs, o.lockdown, *cfg)
	if err != nil {
		return err
	}
	if lock != "" {
		rp.add("lockdown", lock)
		rp.sum.Lockdown = checkLockdownSum{Enabled: !strings.HasPrefix(lock, "off"), Detail: lock}
	}
	if _, _, err := skillsFlag(fs, *o); err != nil {
		return err
	}
	sk := checkSkills(fs, o, *cfg, rp.cfgSrc)
	rp.add("skills", sk.String())
	rp.sum.Skills = sk
	att, err := checkAttempts(fs, o, *cfg)
	if err != nil {
		return err
	}
	if att != "" {
		rp.add("attempts", att)
	}
	if rp.policy != nil {
		ps := rp.policy.Summary()
		rp.sum.Permissions = checkPermissions{Configured: true, Rules: rp.policy.RuleCount(), Default: ps.Default, NonInteractive: ps.NonInteractive, Judge: ps.Judge}
		line := fmt.Sprintf("%d rules, default %s, non-interactive %s", rp.policy.RuleCount(), ps.Default, ps.NonInteractive)
		if ps.Judge {
			line += ", judge on"
		}
		rp.add("permissions", line)
	}
	return nil
}

// modelSource names where resolveModel took the model from.
func modelSource(fs *flag.FlagSet, cfgModel string) string {
	switch {
	case flagWasSet(fs, "model", "m"):
		return "from -m"
	case os.Getenv("AGENT_MODEL") != "":
		return "from $AGENT_MODEL"
	case cfgModel != "":
		return "from config"
	}
	return "default"
}

// limitText renders one limit and, when set, whether a flag or the config
// set it.
func limitText(fs *flag.FlagSet, name string, set bool, value string) string {
	if !set {
		if flagWasSet(fs, name) {
			return "unlimited (from --" + name + ")"
		}
		return "unlimited"
	}
	if flagWasSet(fs, name) {
		return value + " (from --" + name + ")"
	}
	return value + " (from config)"
}

// providerStatus describes spec's provider and its credentials without any
// secret value: whether the key variable is set, Ollama's resolved host, or
// where the Codex login was found.
func providerStatus(spec string, m model.Model) string {
	if d, ok := model.ConfiguredProvider(spec); ok {
		s := fmt.Sprintf("%s (configured, api %s, base_url %s)", d.ID, d.API, d.BaseURL)
		if d.APIKeyEnv == "" {
			return s + ", no api_key_env"
		}
		return s + ", " + d.APIKeyEnv + " is set"
	}
	provider, _, ok := strings.Cut(spec, "/")
	if !ok {
		provider = "openai"
	}
	switch provider {
	case "ollama":
		return "ollama, OLLAMA_HOST resolves to " + model.OllamaBaseURL()
	case "codex":
		if c, ok := m.(*model.Codex); ok {
			return "codex, login found at " + c.AuthFile
		}
		return "codex, login found"
	case model.ClaudeCodeProvider:
		if c, ok := m.(*model.ClaudeCode); ok {
			return "claude-code, runs " + c.Path + " (its own sign-in; not checked)"
		}
		return "claude-code"
	case model.CodexExecProvider:
		if c, ok := m.(*model.CodexExec); ok {
			return "codex-exec, runs " + c.Path + " (its own sign-in; not checked)"
		}
		return "codex-exec"
	case model.OpenCodeProvider:
		if c, ok := m.(*model.OpenCode); ok {
			v := c.Version(context.Background())
			if v == "" {
				v = "version unknown"
			}
			return "opencode, runs " + c.Path + " (" + v + "; its own sign-in; not checked)"
		}
		return "opencode"
	}
	env := map[string]string{"openai": "OPENAI_API_KEY", "openrouter": "OPENROUTER_API_KEY", "anthropic": "ANTHROPIC_API_KEY"}[provider]
	if os.Getenv(env) == "" {
		return provider + ", " + env + " is not set (OPENAI_BASE_URL is set, so the server may not need one)"
	}
	s := provider + ", " + env + " is set"
	if provider == "openai" && os.Getenv("OPENAI_BASE_URL") != "" {
		s += " (OPENAI_BASE_URL is set)"
	}
	return s
}

// checkRoutes lists the routes --check reports: auxiliary tasks routed
// away from the main model, and the delegate and worker tasks when those
// tools are on.
func checkRoutes(cfg config.AgentConfig, main string) []routing.Route {
	out := auxRoutes(cfg, main)
	if cfg.Subagents.Enabled {
		out = append(out, routing.Delegated(cfg, main, routing.TaskDelegate))
	}
	if workersEnabled(cfg) {
		out = append(out, routing.Delegated(cfg, main, routing.TaskWorker))
	}
	return out
}

// checkSandbox returns the --check sandbox line (an unavailable engine is
// reported in the line rather than failing --check): the engine a run would
// use (such as "bwrap + landlock (ABI 6)") and any fallback warning, or the
// error a run would stop on. Empty when --sandbox is off.
func checkSandbox(fs *flag.FlagSet, o options, cfg config.AgentConfig) (string, error) {
	applySandboxFlags(fs, &cfg, o)
	if !cfg.Sandbox || cfg.ReadOnly || cfg.Environment == "container" {
		return "", nil
	}
	backend := cfg.SandboxBackend
	if backend == "" {
		backend = "auto"
	}
	desc, warnings, err := probeSandbox(backend, cfg.SandboxNetwork, cfg.SandboxAllowedDomains)
	if err != nil {
		// Reported, not fatal: --check describes the host; the run itself
		// still fails closed before the first model request.
		return fmt.Sprintf("unavailable (sandbox_backend %s): %v", backend, err), nil
	}
	line := desc + " (sandbox_backend " + backend + ")"
	for _, w := range warnings {
		line += "; warning: " + w
	}
	return line, nil
}

// sandboxBackendName is the sandbox_backend a run would use, "auto" when
// unset.
func sandboxBackendName(cfg config.AgentConfig, fs *flag.FlagSet, o options) string {
	applySandboxFlags(fs, &cfg, o)
	if cfg.SandboxBackend == "" {
		return "auto"
	}
	return cfg.SandboxBackend
}

// probeSandbox is tools.ProbeSandbox; tests replace it.
var probeSandbox = tools.ProbeSandbox

// checkSkills resolves the Agent Skills setting the way a run would and
// reports it with its source (#359). Like a run, chat (-i) and a one-shot
// run on a terminal count as interactive.
func checkSkills(fs *flag.FlagSet, o *options, cfg config.AgentConfig, src config.Source) skillsDecision {
	if flagWasSet(fs, "read-only") {
		cfg.ReadOnly = o.readOnly
	}
	if flagWasSet(fs, "lockdown") {
		cfg.UntrustedContent.Enabled = o.lockdown.enabled
	}
	m := skillsMode{
		Interactive: o.interactive || runIsInteractive(),
		Hardened:    hardenedReason(flagWasSet(fs, "eval"), attemptsActive(fs), cfg.UntrustedContent.Enabled, cfg.ReadOnly),
	}
	return resolveSkills(skillsInputsFor(fs, *o, cfg, src), m)
}
