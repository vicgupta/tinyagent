package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// The chat session core shared by the terminal chat (tinyagent chat) and
// the browser front end (tinyagent web): both set the session up and run
// each turn's bookkeeping here, so they behave the same and differ only in
// how they show a turn and ask the user.

// newChatSession checks the chat flags and sets up a chat session in
// --cwd, loading --resume when given. A nonzero code means it failed and
// the message is already on errw; otherwise the caller must Close s.
func newChatSession(o *options, fs *flag.FlagSet, errw io.Writer) (s *session, prev *trajectory.Trajectory, code int) {
	if _, _, err := skillsFlag(fs, *o); err != nil {
		fmt.Fprintln(errw, "error:", err)
		return nil, nil, 2
	}
	if o.noAgentsMD && o.scopedAgentsMD {
		fmt.Fprintln(errw, "error: --no-agents-md and --scoped-agents-md can't be combined")
		return nil, nil, 2
	}
	if code := checkACPLockdown(o, fs); code != 0 {
		return nil, nil, code
	}
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		fmt.Fprintf(errw, "error: --cwd %s: %v\n", o.cwd, err)
		return nil, nil, 2
	}
	if o.resume != "" {
		if o.output != "" && sameFile(o.resume, o.output) {
			fmt.Fprintln(errw, "error: -o must differ from the --resume checkpoint")
			return nil, nil, 2
		}
		prev, err = trajectory.Load(o.resume)
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return nil, nil, 1
		}
		if !flagWasSet(fs, "model", "m") && prev.Model != "" {
			fs.Set("model", prev.Model)
		}
	}
	s = &session{o: o, fs: fs, root: root, closeEnv: func() {}}
	if err := s.setup(context.Background(), sessionMode{
		interactive: true,
		setupLockdown: func(cfg *config.AgentConfig) (*lockdown.State, error) {
			return acpSetupLockdown(fs, o.lockdown, cfg)
		},
		beforeWebFetch: func(cfg *config.AgentConfig) {
			if flagWasSet(fs, "ask-user") {
				cfg.AskUser.Enabled = o.askUser
			}
		},
		beforeEnvCheck: func(cfg *config.AgentConfig) {
			if cfg.Snapshots.Enabled {
				fmt.Fprintln(errw, "chat: workspace snapshots are not taken in chat mode yet")
				cfg.Snapshots.Enabled = false
			}
		},
		mcpServers: func(cfg config.AgentConfig) ([]config.MCPServer, error) {
			return mcpServers(errw, cfg), nil
		},
		startLockdown: func() { startACPLockdown(s.lockdown, nil, s.env, s.webFetch, errw) },
	}); err != nil {
		s.Close()
		return nil, nil, fail(err)
	}
	if prev != nil {
		if err := agent.ValidateResume(prev, s.cfg, root); err != nil {
			s.Close()
			fmt.Fprintln(errw, "error:", err)
			return nil, nil, 1
		}
	}
	return s, prev, 0
}

// beginTurn does a chat turn's shared bookkeeping before a.Run: the log
// database, trace export and event fan-out, start taints and output files
// on the first turn, skills, project memory, and saving the checkpoint at
// path after every step. The caller closes the returned events.
func (s *session) beginTurn(a *agent.Agent, cfg config.AgentConfig, msg string, prev *trajectory.Trajectory, path string, cancel context.CancelFunc, turns int) *runEvents {
	o := s.o
	var events *runEvents
	warn := func(m string) { events.warn(m) }
	db, dbErr := openLogDB(s.fs, *o, cfg, s.root, msg, s.modelName, prev != nil, a, warn)
	tr, trWarn := startOTel(s.otel, cfg, a, msg, prev != nil, o.verbose && s.turn == 1)
	events = startEvents(io.Discard, false, a, prev != nil, cancel, db, tr)
	if dbErr != nil {
		warn(dbErr.Error())
	}
	if trWarn != "" {
		warn(trWarn)
	}
	events.emit("chat.turn", map[string]any{"turn": turns})
	if s.turn == 1 {
		emitStartTaints(s.lockdown, events)
		startOutputFiles(s.outputFiles, cfg.ToolOutputFiles.MaxRuns, warn)
	}
	if cfg.SkillsOn() {
		loadSkills(a, s.root, pluginSkillSources(cfg), warn)
	}
	s.memoryTurn(a, prev, events, warn, false)
	a.OnStep = func(t *trajectory.Trajectory) error {
		if err := t.Save(path); err != nil {
			return err
		}
		checkpointSaved(a, t, path, o.planFile)
		return nil
	}
	return events
}

// switchModel makes spec the session's model for the next turns, built
// the way setup builds the first one (same transport, handoff options,
// reasoning effort, output cap and fallback chain). On error the session
// keeps its current model.
func (s *session) switchModel(spec string, errw io.Writer) error {
	cfg := s.cfg
	if err := checkReasoningEffort(spec, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := checkMaxOutputTokens(spec, cfg.MaxOutputTokens); err != nil {
		return err
	}
	api := model.OpenAITransport(spec, cfg.OpenAIAPI)
	if err := model.CheckProviderEnv(spec); err != nil {
		return err
	}
	if env := model.MissingKeyEnv(spec); env != "" {
		return fmt.Errorf("%s is not set; set it in the environment and restart the session", env)
	}
	if err := checkFallbacks(spec, s.fallbacks, cfg.OpenAIAPI); err != nil {
		return err
	}
	var onText func(string)
	if !s.o.noStream {
		onText = s.streamed
	}
	m, err := model.NewWith(spec, onText, model.Options{OpenAIAPI: api, Handoff: model.HandoffOptions{Dir: s.root, ReadOnly: cfg.ReadOnly}})
	if err != nil {
		return err
	}
	if err := model.SetReasoningEffort(m, spec, cfg.ReasoningEffort); err != nil {
		return err
	}
	if err := model.SetMaxOutputTokens(m, spec, cfg.MaxOutputTokens); err != nil {
		return err
	}
	warnUnpriced(errw, spec, cfg.CostLimit)
	if m, err = withFallbacks(m, spec, s.fallbacks, onText, cfg, cfg.ReasoningEffort, errw); err != nil {
		return err
	}
	s.model, s.modelName, s.openaiAPI = m, spec, api
	s.providerAPI, s.providerBaseURL = providerInfo(spec)
	return nil
}
