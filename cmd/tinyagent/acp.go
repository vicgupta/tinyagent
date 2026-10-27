package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/acp"
	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/telemetry"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// acpConflicts are the flags --acp can't be combined with: other modes,
// and settings a session gets from the client (the task, its cwd) or that
// need a trajectory file.
var acpConflicts = []string{
	"task", "t", "resume", "rewind", "to", "dry-run", "transcript", "review", "review-base", "review-commit",
	"worktree", "output-schema", "json", "ask-user", "check", "init-config", "completion", "update",
	"version", "V", "output", "o", "cwd", "image", "snapshots", "plan-file",
}

// acpConflict returns the first flag given that --acp can't be combined
// with, as typed (-t, --task), or "".
func acpConflict(fs *flag.FlagSet) string {
	name := ""
	fs.Visit(func(f *flag.Flag) {
		if name == "" && slices.Contains(acpConflicts, f.Name) {
			name = f.Name
		}
	})
	switch {
	case name == "":
		return ""
	case len(name) == 1:
		return "-" + name
	}
	return "--" + name
}

// runACP serves the Agent Client Protocol on in and out until in ends.
// out carries only protocol messages: os.Stdout is pointed at stderr for
// the duration, so no other output can reach it.
func runACP(o *options, fs *flag.FlagSet, in io.Reader, out io.Writer) int {
	if _, _, err := skillsFlag(fs, *o); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if o.noAgentsMD && o.scopedAgentsMD {
		fmt.Fprintln(os.Stderr, "error: --no-agents-md and --scoped-agents-md can't be combined")
		return 2
	}
	defer func(f *os.File) { os.Stdout = f }(os.Stdout)
	os.Stdout = os.Stderr
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := &acp.Server{
		Name: "tinyagent", Version: currentVersion(), Log: os.Stderr,
		NewSession: func(ctx context.Context, id, cwd string, servers []acp.MCPServer) (acp.Session, error) {
			return newACPSession(ctx, o, fs, id, cwd, servers)
		},
	}
	fmt.Fprintln(os.Stderr, "tinyagent: ACP mode (experimental): reading JSON-RPC messages on stdin")
	if err := srv.Serve(ctx, in, out); err != nil {
		return fail(err)
	}
	return 0
}

// acpSession is one ACP session: a working directory with the settings,
// environment, MCP servers and model of a headless run there, and the
// conversation so far. Each prompt is one Agent.Run; later ones continue
// the previous turn's trajectory like a --resume -t follow-up.
type acpSession struct {
	id   string
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
	notes                        []string // shown before the first turn
	traj                         *trajectory.Trajectory
	turn                         int
	textMu                       sync.Mutex
	text                         func(string) // the running turn's streamed text
}

// newACPSession sets a session up the way run sets up a headless run in
// cwd, from the same config and flags. Errors carry the message (and any
// hint) a headless run prints; the client sees them as JSON-RPC errors.
func newACPSession(ctx context.Context, o *options, fs *flag.FlagSet, id, cwd string, servers []acp.MCPServer) (_ *acpSession, err error) {
	root, err := canonicalRoot(cwd)
	if err != nil {
		return nil, acp.InvalidParams("cwd %s is not an existing directory: %v", cwd, err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, acp.InvalidParams("cwd %s is not an existing directory", cwd)
	}
	s := &acpSession{id: id, o: o, fs: fs, root: root, closeEnv: func() {}}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	cfgPath := config.Find(o.configPath)
	cfg, err := config.LoadProfile(cfgPath, o.profile)
	if err != nil {
		return nil, err
	}
	registerProviders(cfg)
	if s.hooks, err = hooks.New(cfg.Hooks, cfgPath); err != nil {
		return nil, err
	}
	if s.policy, err = permissions.New(cfg.Permissions); err != nil {
		return nil, err
	}
	if s.otel, err = otelSettings(fs, *o, cfg); err != nil {
		return nil, err
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
		return nil, err
	}
	if flagWasSet(fs, "web-fetch") {
		cfg.WebFetch.Enabled = o.webFetch
	}
	// A question is answered with --resume, which ACP sessions don't use.
	if cfg.AskUser.Enabled {
		fmt.Fprintln(os.Stderr, "acp: ask_user is not available in --acp mode; the tool is off")
		cfg.AskUser.Enabled = false
	}
	if s.webFetch, err = newWebFetcher(fs, *o, &cfg); err != nil {
		return nil, err
	}
	if flagWasSet(fs, "lsp") {
		cfg.LSP.Enabled = o.lsp
	}
	if flagWasSet(fs, "scoped-agents-md") {
		cfg.ScopedAgentsMD = o.scopedAgentsMD
	}
	if err := lspError(cfg, cfgPath); err != nil {
		return nil, err
	}
	if on, set, _ := skillsFlag(fs, *o); set {
		cfg.Skills = on
	}
	// Snapshots are indexed by a trajectory file, which ACP sessions don't
	// write.
	if cfg.Snapshots.Enabled {
		fmt.Fprintln(os.Stderr, "acp: workspace snapshots are not taken in --acp mode")
		cfg.Snapshots.Enabled = false
	}
	if msg := envConflict(cfg); msg != "" {
		return nil, errors.New(msg)
	}
	s.modelName = resolveModel(fs, o.modelName, cfg.Model)
	if flagWasSet(fs, "reasoning-effort") {
		cfg.ReasoningEffort = o.reasoningEffort
	}
	if err := checkReasoningEffort(s.modelName, cfg.ReasoningEffort); err != nil {
		return nil, err
	}
	if err := checkMaxOutputTokens(s.modelName, cfg.MaxOutputTokens); err != nil {
		return nil, err
	}
	if flagWasSet(fs, "openai-api") {
		cfg.OpenAIAPI = o.openaiAPI
	}
	if err := checkOpenAIAPI(cfg.OpenAIAPI); err != nil {
		return nil, err
	}
	s.openaiAPI = model.OpenAITransport(s.modelName, cfg.OpenAIAPI)
	if err := model.CheckProviderEnv(s.modelName); err != nil {
		return nil, withHint(s.modelName, err.Error(), err.Error())
	}
	if env := model.MissingKeyEnv(s.modelName); env != "" {
		return nil, withHint(s.modelName, fmt.Sprintf("%s is not set; set it in the agent's env in the editor's agent settings", env), "HTTP 401: ")
	}
	if s.fallbacks, err = fallbackModels(fs, o.fallbackModels, o.noFallback, cfg); err == nil {
		err = checkFallbacks(s.modelName, s.fallbacks, cfg.OpenAIAPI)
	}
	if err != nil {
		return nil, err
	}
	if s.subagents, err = newSubagents(cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return nil, err
	}
	if s.workers, err = newWorkers(cfg, s.modelName, cfg.ReasoningEffort); err != nil {
		return nil, err
	}
	model.PriceOverride = [2]float64{cfg.InputPricePerMTok, cfg.OutputPricePerMTok}
	if s.outputFiles, err = toolOutputFiles(fs, *o, cfg, root); err != nil {
		return nil, err
	}
	if s.fileAccess, err = newFileAccess(cfg, root, s.outputFiles, o.verbose); err != nil {
		return nil, err
	}
	if s.extraDirs, err = additionalDirs(fs, *o, cfg, root, o.verbose); err != nil {
		return nil, err
	}
	s.fileAccess.AddDirs(s.extraDirs)
	if cfg.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: only read_file, list_files, and grep are available")
	}
	mcpDefs, err := s.mcpServers(cfg, servers)
	if err != nil {
		return nil, err
	}
	env, closeEnv, err := startEnvironment(ctx, cfg, root, s.extraDirs, o.verbose)
	if err != nil {
		return nil, err
	}
	s.env, s.closeEnv = env, closeEnv
	if s.mcp, err = mcptools.Start(ctx, root, mcpDefs, os.Stderr); err != nil {
		return nil, err
	}

	var onText func(string)
	if !o.noStream {
		onText = s.streamed
	}
	announceProfile(os.Stderr, o.profile, cfgPath, o.verbose)
	announceProvider(os.Stderr, s.modelName, o.verbose)
	if s.model, err = model.NewWith(s.modelName, onText, model.Options{OpenAIAPI: s.openaiAPI}); err != nil {
		return nil, withHint(s.modelName, err.Error(), err.Error())
	}
	if err := model.SetReasoningEffort(s.model, s.modelName, cfg.ReasoningEffort); err != nil {
		return nil, err
	}
	if err := model.SetMaxOutputTokens(s.model, s.modelName, cfg.MaxOutputTokens); err != nil {
		return nil, err
	}
	warnUnpriced(os.Stderr, s.modelName, cfg.CostLimit)
	if s.model, err = withFallbacks(s.model, s.modelName, s.fallbacks, onText, cfg, cfg.ReasoningEffort, os.Stderr); err != nil {
		return nil, err
	}
	s.providerAPI, s.providerBaseURL = providerInfo(s.modelName)
	if flagWasSet(fs, "redact-secrets") {
		cfg.RedactSecrets.Enabled = o.redactSecrets
	}
	s.redactor, s.redactInfo = newSecretRedactor(cfg, os.Environ(), os.Stderr, o.verbose)
	s.cfg = cfg
	fmt.Fprintf(os.Stderr, "acp: session %s in %s with %s\n", id, root, s.modelName)
	return s, nil
}

// withHint is msg followed by the hint a headless run prints for errMsg.
func withHint(spec, msg, errMsg string) error {
	if h := model.Hint(spec, errMsg); h != "" {
		msg += "\nhint: " + h
	}
	return errors.New(msg)
}

// mcpServers returns the MCP servers to start: the config's (none when
// read-only) and, with acp.client_mcp_servers, the client's. Ignored client
// servers are named in a note before the first turn. Values of the
// client's env entries are never printed.
func (s *acpSession) mcpServers(cfg config.AgentConfig, client []acp.MCPServer) ([]config.MCPServer, error) {
	out := mcpServers(os.Stderr, cfg)
	if len(client) == 0 {
		return out, nil
	}
	names := make([]string, len(client))
	for i, c := range client {
		names[i] = c.Name
	}
	switch {
	case !cfg.ACP.ClientMCPServers:
		s.notes = append(s.notes, fmt.Sprintf("Note: tinyagent did not start the MCP servers the editor offered (%s). Client-supplied MCP servers are off by default; set acp.client_mcp_servers: true in the tinyagent user config (or the -c file) to start them.", strings.Join(names, ", ")))
		fmt.Fprintf(os.Stderr, "acp: ignoring client MCP servers (%s); acp.client_mcp_servers is off\n", strings.Join(names, ", "))
		return out, nil
	case cfg.ReadOnly:
		s.notes = append(s.notes, fmt.Sprintf("Note: read-only mode: tinyagent did not start the MCP servers the editor offered (%s).", strings.Join(names, ", ")))
		return out, nil
	}
	used := map[string]bool{}
	for _, d := range out {
		used[d.Alias] = true
	}
	for _, c := range client {
		if c.Type != "" && c.Type != "stdio" {
			return nil, acp.InvalidParams("mcpServers %q: %s servers are not supported; tinyagent connects to stdio MCP servers only", c.Name, c.Type)
		}
		def := config.MCPServer{Alias: mcpAlias(c.Name, used), Command: c.Command, Args: c.Args, AllTools: true}
		for _, e := range c.Env {
			if e.Name == "" || strings.ContainsAny(e.Name, "=\x00") {
				return nil, acp.InvalidParams("mcpServers %q: invalid env variable name %q", c.Name, e.Name)
			}
			def.SetEnv = append(def.SetEnv, e.Name+"="+e.Value)
		}
		out = append(out, def)
	}
	return out, nil
}

// mcpAlias turns a client's server name into a unique tool-name prefix.
func mcpAlias(name string, used map[string]bool) string {
	b := []byte(name)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	alias := string(b)
	if len(alias) > 24 {
		alias = alias[:24]
	}
	if alias == "" {
		alias = "client"
	}
	base := alias
	for n := 2; used[alias]; n++ {
		alias = fmt.Sprintf("%s_%d", base, n)
	}
	used[alias] = true
	return alias
}

// streamed forwards the model's streamed text to the running turn.
func (s *acpSession) streamed(text string) {
	s.textMu.Lock()
	f := s.text
	s.textMu.Unlock()
	if f != nil {
		f(text)
	}
}

func (s *acpSession) setText(f func(string)) {
	s.textMu.Lock()
	s.text = f
	s.textMu.Unlock()
}

// Close stops the session's MCP servers and releases its environment.
// Background processes and language servers already stopped with the last
// turn.
func (s *acpSession) Close() {
	if s.mcp != nil {
		s.mcp.Close()
	}
	s.closeEnv()
}

// Prompt runs one turn. The first is the session's task; later ones are
// follow-ups to the conversation so far. Limits apply to each turn.
func (s *acpSession) Prompt(ctx context.Context, p acp.Prompt, u *acp.Updates) (string, error) {
	images, err := s.promptImages(p.Images)
	if err != nil {
		return "", err
	}
	s.turn++
	cfg := s.cfg
	prev := s.traj
	if prev != nil {
		if cfg.StepLimit > 0 {
			cfg.StepLimit += prev.Steps
		}
		if cfg.CostLimit > 0 {
			cfg.CostLimit += prev.Cost
		}
	}
	if s.turn == 1 {
		for _, n := range s.notes {
			u.Message(n + "\n\n")
		}
	}
	o := s.o
	a := &agent.Agent{
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
			ExternalSpecs:      s.mcp.Specs,
			ExternalCall:       s.mcp.Call,
			Plan:               cfg.Plan.Enabled,
			Images:             cfg.Images.Enabled,
			ApplyPatch:         cfg.ApplyPatch,
			Delegate:           s.subagents != nil,
			Workers:            s.workers != nil,
			WebFetch:           s.webFetch,
			LSP:                newLSP(cfg, s.root), // a turn's end stops them
			OutputFiles:        s.outputFiles,
			FileAccess:         s.fileAccess,
			ExtraDirs:          s.extraDirs,
			MaxImageBytes:      cfg.MaxImageBytes,
			ExternalCallImages: s.mcp.CallImages(cfg.MaxImageBytes),
			Redactor:           s.redactor,
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
		TaskImages:     images,
	}
	if prev != nil {
		a.Resume, a.ResumeSource, a.FollowUp, a.Continue = prev, "acp session "+s.id, p.Text, true
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if s.policy != nil {
		a.PermissionApprover = s.permissionApprover(u, cancel)
	}
	var events *runEvents
	warn := func(msg string) { events.warn(msg) }
	db, dbErr := openLogDB(s.fs, *o, cfg, s.root, p.Text, s.modelName, prev != nil, a, warn)
	tr, trWarn := startOTel(s.otel, cfg, a, p.Text, prev != nil, o.verbose && s.turn == 1)
	events = startEvents(io.Discard, false, a, prev != nil, cancel, db, tr)
	defer events.close()
	if dbErr != nil {
		warn(dbErr.Error())
	}
	if trWarn != "" {
		warn(trWarn)
	}
	events.emit("acp.turn", map[string]any{"session_id": s.id, "turn": s.turn})
	if s.turn == 1 {
		startOutputFiles(s.outputFiles, cfg.ToolOutputFiles.MaxRuns, warn)
	}
	if cfg.Skills {
		loadSkills(a, s.root, warn)
	}
	turn := newACPTurn(u, s, a, prev)
	logged := a.OnEvent
	a.OnEvent = func(kind string, fields map[string]any) {
		if logged != nil {
			logged(kind, fields)
		}
		turn.event(kind, fields)
	}
	a.OnStep = turn.step
	text := &markerFilter{emit: u.Message}
	turn.flush = text.flush
	s.setText(text.write)
	start := time.Now()
	t := a.Run(ctx, p.Text)
	s.setText(nil)
	text.flush()
	elapsed := time.Since(start)
	reportSummary(os.Stderr, s.modelName, t, elapsed, "")
	events.finish(t, elapsed)
	s.traj = t
	return s.stopReason(t, elapsed, u)
}

// permissionApprover answers a policy ask through the editor's
// session/request_permission, with allow_once, allow_always and reject_once
// options. A "cancelled" outcome cancels the turn like session/cancel.
func (s *acpSession) permissionApprover(u *acp.Updates, cancel context.CancelFunc) func(ctx context.Context, req permissions.Request) permissions.Answer {
	return func(ctx context.Context, req permissions.Request) permissions.Answer {
		params := map[string]any{
			"sessionId": s.id,
			"toolCall":  map[string]any{"toolCallId": req.ToolCallID},
			"options": []map[string]any{
				{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
				{"optionId": "allow-always", "name": "Allow always", "kind": "allow_always"},
				{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
			},
		}
		result, err := u.Request(ctx, "session/request_permission", params)
		if err != nil {
			// Interrupted, or the client returned a JSON-RPC error; reject.
			return permissions.Answer{Decision: permissions.DecisionDeny, Channel: "acp"}
		}
		var out struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		if json.Unmarshal(result, &out) != nil {
			return permissions.Answer{Decision: permissions.DecisionDeny, Channel: "acp"}
		}
		switch {
		case out.Outcome.Outcome == "cancelled":
			cancel()
			return permissions.Answer{Decision: permissions.DecisionDeny, Channel: "acp"}
		case out.Outcome.OptionID == "allow-always":
			return permissions.Answer{Decision: permissions.DecisionAllow, Always: true, Channel: "acp"}
		case out.Outcome.OptionID == "allow-once":
			return permissions.Answer{Decision: permissions.DecisionAllow, Channel: "acp"}
		default:
			return permissions.Answer{Decision: permissions.DecisionDeny, Channel: "acp"}
		}
	}
}

// promptImages validates a prompt's images like --image attachments.
func (s *acpSession) promptImages(in []acp.Image) ([]model.Image, error) {
	if len(in) > model.MaxTaskImages {
		return nil, acp.InvalidParams("prompt has %d images; at most %d may be attached", len(in), model.MaxTaskImages)
	}
	maxBytes := s.cfg.MaxImageBytes
	if maxBytes <= 0 {
		maxBytes = tools.DefaultMaxImageBytes
	}
	var out []model.Image
	for i, im := range in {
		if len(im.Data) > maxBytes {
			return nil, acp.InvalidParams("image %d (%s) is %s bytes; max_image_bytes is %s", i+1, im.Label, model.FormatBytes(len(im.Data)), model.FormatBytes(maxBytes))
		}
		mime := tools.SniffImage(im.Data)
		if mime == "" {
			return nil, acp.InvalidParams("image %d (%s) is not a PNG, JPEG, GIF, or WebP image", i+1, im.Label)
		}
		out = append(out, model.NewImage(mime, im.Label, im.Data))
	}
	return out, nil
}

// stopReason maps a turn's status to its ACP stop reason. Statuses ACP has
// no reason for end the turn after a message naming them; an error a
// headless run would print a setup hint for is returned as an error.
func (s *acpSession) stopReason(t *trajectory.Trajectory, elapsed time.Duration, u *acp.Updates) (string, error) {
	switch t.ExitStatus {
	case agent.StatusSubmitted, agent.StatusCompleted:
		return acp.StopEndTurn, nil
	case agent.StatusStepLimit:
		return acp.StopMaxTurnRequests, nil
	case agent.StatusOutputTruncated:
		return acp.StopMaxTokens, nil
	case agent.StatusInterrupted:
		return acp.StopCancelled, nil
	case agent.StatusError:
		if h := model.Hint(s.modelName, t.Submission); h != "" {
			return "", errors.New(t.Submission + "\nhint: " + h)
		}
	}
	u.Message("\n\ntinyagent stopped: " + summaryLine(t, elapsed))
	return acp.StopEndTurn, nil
}

// acpTurn turns one turn's run events and steps into session updates.
type acpTurn struct {
	u        *acp.Updates
	root     string
	streamed bool
	window   int
	// announced holds the tool calls sent as tool_call.
	announced map[string]bool
	// flush sends streamed text held back so far.
	flush       func()
	planVersion int
	steps       int
	in, out     int
}

func newACPTurn(u *acp.Updates, s *acpSession, a *agent.Agent, prev *trajectory.Trajectory) *acpTurn {
	t := &acpTurn{u: u, root: s.root, streamed: !s.o.noStream, announced: map[string]bool{}}
	t.window = s.cfg.ContextWindow
	if t.window <= 0 {
		t.window = model.ContextWindow(a.ModelName)
	}
	if prev != nil {
		t.steps, t.in, t.out = prev.Steps, prev.InputTokens, prev.OutputTokens
		if prev.Plan != nil {
			t.planVersion = prev.Plan.Version
		}
	}
	return t
}

func (t *acpTurn) event(kind string, f map[string]any) {
	id, _ := f["id"].(string)
	switch kind {
	case "assistant.message":
		if !t.streamed {
			text, _ := f["text"].(string)
			t.u.Message(strings.TrimSpace(strings.Replace(text, agent.SubmitMarker, "", 1)))
		}
	case "tool.call":
		name, _ := f["name"].(string)
		args := f["arguments"]
		t.flush()
		t.announced[id] = true
		t.u.ToolCall(id, toolTitle(name, args), toolKind(name), t.locations(name, args), args)
		t.u.ToolCallStatus(id, "in_progress", "")
	case "tool.result":
		if !t.announced[id] {
			return // a cut-off response's calls never ran
		}
		status := "completed"
		if failed, _ := f["is_error"].(bool); failed {
			status = "failed"
		}
		out, _ := f["output"].(string)
		t.u.ToolCallStatus(id, status, out)
	}
}

// step sends plan changes and, after a model call, the context usage.
func (t *acpTurn) step(tr *trajectory.Trajectory) error {
	if p := tr.Plan; p != nil && p.Version != t.planVersion {
		t.planVersion = p.Version
		entries := make([]acp.PlanEntry, len(p.Items))
		for i, it := range p.Items {
			status := "pending"
			switch it.Status {
			case "in_progress":
				status = "in_progress"
			case "done":
				status = "completed"
			}
			entries[i] = acp.PlanEntry{Content: it.Text, Priority: "medium", Status: status}
		}
		t.u.Plan(entries)
	}
	if tr.Steps != t.steps {
		used := tr.InputTokens - t.in + tr.OutputTokens - t.out
		t.steps, t.in, t.out = tr.Steps, tr.InputTokens, tr.OutputTokens
		if t.window > 0 && used > 0 {
			t.u.Usage(used, t.window, tr.Cost)
		}
	}
	return nil
}

// toolKind is the ACP kind of a tool, for the client's icon.
func toolKind(name string) string {
	switch name {
	case "read_file":
		return "read"
	case "write_file", "edit_file", "apply_patch":
		return "edit"
	case "grep", "list_files":
		return "search"
	case "bash", "start_process":
		return "execute"
	case "web_fetch":
		return "fetch"
	}
	return "other"
}

// toolTitle is the tool name with its main argument, on one short line.
func toolTitle(name string, args any) string {
	m, _ := args.(map[string]any)
	for _, k := range []string{"command", "path", "pattern", "url", "task", "id"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			v = strings.Join(strings.Fields(v), " ")
			if utf8.RuneCountInString(v) > 80 {
				v = string([]rune(v)[:79]) + "…"
			}
			return name + ": " + v
		}
	}
	return name
}

// locations returns the absolute paths a built-in file tool call touches.
func (t *acpTurn) locations(name string, args any) []string {
	switch name {
	case "read_file", "write_file", "edit_file", "list_files", "grep", tools.ViewImageToolName:
	default:
		return nil
	}
	m, _ := args.(map[string]any)
	p, _ := m["path"].(string)
	switch {
	case p == "" && (name == "list_files" || name == "grep"):
		return []string{t.root}
	case p == "":
		return nil
	case !filepath.IsAbs(p):
		p = filepath.Join(t.root, p)
	}
	return []string{filepath.Clean(p)}
}

// markerFilter passes streamed text on without the submit marker, holding
// back a tail that may be the start of one until more text or flush.
type markerFilter struct {
	emit func(string)
	buf  string
}

func (f *markerFilter) write(s string) {
	f.buf = strings.ReplaceAll(f.buf+s, agent.SubmitMarker, "")
	keep := 0
	for n := min(len(agent.SubmitMarker)-1, len(f.buf)); n > 0; n-- {
		if strings.HasSuffix(f.buf, agent.SubmitMarker[:n]) {
			keep = n
			break
		}
	}
	f.emit(f.buf[:len(f.buf)-keep])
	f.buf = f.buf[len(f.buf)-keep:]
}

func (f *markerFilter) flush() {
	f.emit(f.buf)
	f.buf = ""
}
