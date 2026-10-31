package main

import (
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
	r, err := resolveCheck(o, fs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var hint checkHint
		if errors.As(err, &hint) {
			printHint(os.Stderr, hint.spec, hint.msg)
		}
		return 2
	}
	for _, l := range r {
		fmt.Fprintf(w, "%-10s %s\n", l[0]+":", l[1])
	}
	fmt.Fprintln(w, "ok")
	return 0
}

// checkHint marks an error that has a setup hint: the hint is looked up for
// spec as if a request had failed with msg.
type checkHint struct {
	spec, msg string
	err       error
}

func (h checkHint) Error() string { return h.err.Error() }

// resolveCheck returns the --check report lines, or the first error a run
// would stop on before its first request.
func resolveCheck(o *options, fs *flag.FlagSet) ([][2]string, error) {
	cfgPath := config.Find(o.configPath)
	cfg, err := config.LoadProfile(cfgPath, o.profile)
	if err == nil {
		err = applyMemoryConfig(&cfg, o.cwd, io.Discard)
	}
	if err != nil {
		return nil, err
	}
	registerProviders(cfg)
	if _, err := hooks.New(cfg.Hooks, cfgPath); err != nil {
		return nil, err
	}
	policy, err := permissions.New(cfg.Permissions)
	if err != nil {
		return nil, err
	}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if flagWasSet(fs, "max-output-tokens") {
		cfg.MaxOutputTokens = o.maxOutputTokens
	}
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
		return nil, err
	}
	if err := checkMaxOutputTokens(spec, cfg.MaxOutputTokens); err != nil {
		return nil, err
	}
	if flagWasSet(fs, "openai-api") {
		cfg.OpenAIAPI = o.openaiAPI
	}
	if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
		return nil, err
	}
	if err := model.CheckProviderEnv(spec); err != nil {
		return nil, err
	}
	fallbacks, err := fallbackModels(fs, o.fallbackModels, o.noFallback, cfg)
	if err == nil {
		err = checkFallbacks(spec, fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		return nil, err
	}
	// Building the client makes no request; it reports an unknown provider,
	// a missing model name or a missing Codex login, as a run would.
	m, err := model.NewWith(spec, nil, model.Options{OpenAIAPI: model.OpenAITransport(spec, cfg.OpenAIAPI)})
	if err != nil {
		return nil, checkHint{spec: spec, msg: err.Error(), err: err}
	}
	if env := model.MissingKeyEnv(spec); env != "" {
		return nil, checkHint{spec: spec, msg: "HTTP 401: ", err: fmt.Errorf("%s is not set", env)}
	}
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("--cwd %s is not a directory", o.cwd)
	}
	cmdTools, err := tools.NewCommandTools(cfg.CommandTools, root, cfg.BashTimeoutSec)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	var lines [][2]string
	add := func(k, v string) { lines = append(lines, [2]string{k, v}) }
	switch {
	case o.configPath != "":
		add("config", cfgPath+" (from -c)")
	case cfgPath != "":
		add("config", cfgPath+" (user config)")
	default:
		add("config", "none (built-in defaults)")
	}
	if o.profile != "" {
		add("profile", o.profile)
	}
	add("model", spec+" ("+specSource+")")
	add("provider", providerStatus(spec, m))
	switch {
	case len(fallbacks) == 0 && o.noFallback:
		add("fallback", "none (--no-fallback)")
	case len(fallbacks) == 0:
		add("fallback", "none")
	case flagWasSet(fs, "fallback-model"):
		add("fallback", strings.Join(fallbacks, ", ")+" (from --fallback-model)")
	default:
		add("fallback", strings.Join(fallbacks, ", ")+" (from config)")
	}
	if flagWasSet(fs, "subagents") {
		cfg.Subagents.Enabled = o.subagents
	}
	if flagWasSet(fs, "workers") {
		setWorkers(&cfg, o.workers)
	}
	for _, r := range checkRoutes(cfg, spec) {
		if r.Model != spec || len(r.Fallbacks) > 0 {
			if _, err := routedModel(cfg, r, spec, cfg.ReasoningEffort); err != nil {
				return nil, err
			}
		}
		line := r.Model + " (" + r.Role + ", from " + r.Source + ")"
		if len(r.Fallbacks) > 0 {
			line += ", fallback " + strings.Join(r.Fallbacks, " -> ")
		}
		add(r.Task, line)
	}
	if cfg.Subagents.Enabled || workersEnabled(cfg) {
		if _, err := childRoutes(cfg, spec, routing.TaskDelegate, cfg.ReasoningEffort); err != nil {
			return nil, err
		}
		for i, mr := range cfg.Models.Delegated.Routes {
			var when []string
			if len(mr.Match) > 0 {
				when = append(when, "match "+strings.Join(mr.Match, ", "))
			}
			if mr.Type != "" {
				when = append(when, "type "+mr.Type)
			}
			add("route", fmt.Sprintf("%d: %s -> %s", i+1, strings.Join(when, " or "), mr.Model))
		}
	} else if cfg.Models.Flags.NoRouting {
		add("route", "off (--no-routing)")
	}
	cwdSource := "default"
	if flagWasSet(fs, "cwd") {
		cwdSource = "from --cwd"
	}
	add("cwd", root+" ("+cwdSource+")")
	add("limits", strings.Join([]string{
		"steps " + limitText(fs, "step-limit", cfg.StepLimit > 0, fmt.Sprint(cfg.StepLimit)),
		"cost " + limitText(fs, "cost-limit", cfg.CostLimit > 0, formatCost(cfg.CostLimit)),
		"tokens " + limitText(fs, "token-limit", cfg.TokenLimit > 0, fmt.Sprint(cfg.TokenLimit)),
		"time " + limitText(fs, "time-limit", cfg.WallTimeSec > 0, fmt.Sprintf("%ds", cfg.WallTimeSec)),
	}, ", "))
	for _, s := range cfg.MCPServers {
		add("mcp", fmt.Sprintf("%s: %d tools, %d resource patterns", s.Alias, len(s.Tools), len(s.Resources)))
	}
	pl, err := checkPluginsLine(cfg, os.Stderr)
	if err != nil {
		return nil, err
	}
	if pl != "" {
		add("plugins", pl)
	}
	if flagWasSet(fs, "ask-user") {
		cfg.AskUser.Enabled = o.askUser
	}
	switch {
	case cfg.AskUser.Enabled && flagWasSet(fs, "ask-user"):
		add("ask_user", fmt.Sprintf("on, at most %d questions per run (from --ask-user)", cfg.AskUser.MaxQuestions))
	case cfg.AskUser.Enabled:
		add("ask_user", fmt.Sprintf("on, at most %d questions per run (from config)", cfg.AskUser.MaxQuestions))
	case flagWasSet(fs, "ask-user"):
		add("ask_user", "off (from --ask-user)")
	}
	if n := len(cmdTools); n > 0 {
		names := make([]string, n)
		for i, c := range cmdTools {
			names[i] = c.Name
		}
		add("tools", fmt.Sprintf("%d command tools (%s)", n, strings.Join(names, ", ")))
	}
	if sb, err := checkSandbox(fs, *o, cfg); err != nil {
		return nil, err
	} else if sb != "" {
		add("sandbox", sb)
	}
	if mem := checkMemory(fs, o, cfg.Memory, root); mem != "" {
		add("memory", mem)
	}
	lock, err := checkLockdown(fs, o.lockdown, cfg)
	if err != nil {
		return nil, err
	}
	if lock != "" {
		add("lockdown", lock)
	}
	att, err := checkAttempts(fs, o, cfg)
	if err != nil {
		return nil, err
	}
	if att != "" {
		add("attempts", att)
	}
	if policy != nil {
		add("permissions", fmt.Sprintf("%d rules, default %s, non-interactive %s", policy.RuleCount(), policy.Summary().Default, policy.Summary().NonInteractive))
	}
	return lines, nil
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

// probeSandbox is tools.ProbeSandbox; tests replace it.
var probeSandbox = tools.ProbeSandbox
