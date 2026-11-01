package main

import (
	"context"
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

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/playbooks"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// chatConflicts are the flags --interactive can't be combined with: other
// modes, and output formats meant for scripts.
var chatConflicts = []string{
	"acp", "json", "workflow", "workflow-name", "workflow-observe", "eval", "review", "review-base", "review-commit",
	"rewind", "to", "dry-run", "transcript", "output-schema", "check", "init-config", "completion",
	"update", "version", "V", "worktree", "image",
}

// chatConflict returns the first flag given that --interactive can't be
// combined with, as typed, or "".
func chatConflict(fs *flag.FlagSet) string {
	name := ""
	fs.Visit(func(f *flag.Flag) {
		if name == "" && slices.Contains(chatConflicts, f.Name) {
			name = f.Name
		}
	})
	if name == "" && attemptsActive(fs) {
		return "--attempts"
	}
	switch {
	case name == "":
		return ""
	case len(name) == 1:
		return "-" + name
	}
	return "--" + name
}

// chatTerminal reports whether stdin and stdout are both terminals; tests
// replace it.
var chatTerminal = func() bool { return stdinIsTerminal() && isTerminal(os.Stdout) }

// chatIO is where a chat session reads and writes: the terminal, or a
// test's pipes. interrupts delivers Ctrl-C (SIGINT) while a turn runs.
type chatIO struct {
	in         *os.File // raw mode is tried on it when it is a terminal
	reader     io.Reader
	out, errw  io.Writer
	interrupts chan os.Signal
}

// runChat runs the interactive chat mode (tinyagent chat, -i).
func runChat(o *options, fs *flag.FlagSet) int {
	if o.task == "-" {
		fmt.Fprintln(os.Stderr, "error: chat reads messages from the terminal; -t - (task on stdin) can't be used")
		return 2
	}
	if !chatTerminal() {
		fmt.Fprintln(os.Stderr, "error: chat needs an interactive terminal on stdin and stdout; for scripts run tinyagent -t \"<task>\" (with --resume to continue)")
		return 2
	}
	if !enableEscapes(os.Stdout) || !enableEscapes(os.Stderr) {
		// A console without ANSI support would show escapes as text.
		stdoutStyle, stderrStyle = styler{}, styler{}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	return runChatIO(o, fs, chatIO{in: os.Stdin, reader: os.Stdin, out: os.Stdout, errw: os.Stderr, interrupts: sig})
}

// chat is one interactive session: a session plus the terminal state.
type chat struct {
	s       *session
	io      chatIO
	ed      *lineEditor
	st      styler // stderr's
	outSt   styler // stdout's, for replies
	md      bool   // render replies as Markdown (/raw turns it off)
	path    string // checkpoint written after every step
	source  string // ResumeSource for the next turn
	cont    bool   // the next turn continues s.traj (Continue)
	compact bool   // /compact: compact before the next request
	// playbook is set by /playbook and applies to the next turn only.
	playbook *playbooks.Playbook
	turns    int
	start    time.Time

	mu         sync.Mutex
	cancel     context.CancelFunc // the running turn's, or nil at the prompt
	lastPrompt time.Time          // last Ctrl-C at an empty prompt
}

func runChatIO(o *options, fs *flag.FlagSet, cio chatIO) int {
	errw := cio.errw
	if _, _, err := skillsFlag(fs, *o); err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 2
	}
	if o.noAgentsMD && o.scopedAgentsMD {
		fmt.Fprintln(errw, "error: --no-agents-md and --scoped-agents-md can't be combined")
		return 2
	}
	if code := checkACPLockdown(o, fs); code != 0 {
		return code
	}
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		fmt.Fprintf(errw, "error: --cwd %s: %v\n", o.cwd, err)
		return 2
	}
	var prev *trajectory.Trajectory
	if o.resume != "" {
		if o.output != "" && sameFile(o.resume, o.output) {
			fmt.Fprintln(errw, "error: -o must differ from the --resume checkpoint")
			return 2
		}
		prev, err = trajectory.Load(o.resume)
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		if !flagWasSet(fs, "model", "m") && prev.Model != "" {
			fs.Set("model", prev.Model)
		}
	}
	s := &session{o: o, fs: fs, root: root, closeEnv: func() {}}
	defer s.Close()
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
		return fail(err)
	}
	if prev != nil {
		if err := agent.ValidateResume(prev, s.cfg, root); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
	}
	c := &chat{s: s, io: cio, st: styleFor(errw), outSt: styleFor(cio.out), start: time.Now()}
	c.md = s.cfg.ChatMarkdown == nil || *s.cfg.ChatMarkdown
	c.path = o.output
	if c.path == "" {
		if c.path, err = newChatPath(root); err != nil {
			return fail(err)
		}
	}
	histPath := ""
	if !o.noChatHistory && (s.cfg.ChatHistory == nil || *s.cfg.ChatHistory) {
		histPath = filepath.Join(root, chatHistoryPath)
	}
	hist, err := loadChatHistory(histPath)
	if err != nil {
		fmt.Fprintln(errw, "chat: history not loaded:", err)
	}
	c.ed = newLineEditor(cio.in, cio.reader, cio.out, hist)
	go c.watchInterrupts()

	io.WriteString(errw, c.banner())
	if prev != nil {
		s.traj, c.source = prev, o.resume
		c.turns = 0
		switch {
		case chatFinished(prev):
			c.cont = true
		default:
			fmt.Fprintf(errw, "checkpoint %s stopped with %s; continue that run first? [Y/n] ", o.resume, prev.ExitStatus)
			ans, err := c.ed.ReadLine("")
			if err != nil {
				return c.exit()
			}
			if a := strings.ToLower(strings.TrimSpace(ans)); a == "" || a == "y" || a == "yes" {
				c.turn(prev.Task, false)
			} else {
				// A follow-up continues the run whatever its status.
				c.cont = true
			}
		}
	}
	if o.task != "" {
		c.ed.hist.add(o.task)
		c.turn(o.task, true)
	}
	for {
		msg, err := c.readMessage()
		switch {
		case errors.Is(err, errInterrupt):
			if strings.TrimSpace(msg) != "" {
				continue // Ctrl-C with text typed clears the line
			}
			if c.promptInterrupt() {
				return c.exit()
			}
			continue
		case err != nil:
			return c.exit()
		}
		c.lastPrompt = time.Time{}
		if strings.TrimSpace(msg) == "" {
			continue
		}
		c.ed.hist.add(msg)
		if cmd := strings.TrimSpace(msg); strings.HasPrefix(cmd, "/") && !strings.HasPrefix(cmd, "//") {
			if c.command(cmd) {
				return c.exit()
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(msg), "//") {
			msg = strings.Replace(msg, "//", "/", 1) // "//x" sends "/x"
		}
		c.turn(msg, true)
	}
}

// chatFinished reports whether t ended in a state a follow-up continues
// cleanly (as opposed to a run stopped mid-task).
func chatFinished(t *trajectory.Trajectory) bool {
	return t.ExitStatus == agent.StatusSubmitted || t.ExitStatus == agent.StatusCompleted || t.ExitStatus == agent.StatusNeedsInput
}

// promptInterrupt handles Ctrl-C at an empty prompt: the second within two
// seconds exits.
func (c *chat) promptInterrupt() bool {
	now := time.Now()
	if !c.lastPrompt.IsZero() && now.Sub(c.lastPrompt) < 2*time.Second {
		return true
	}
	c.lastPrompt = now
	fmt.Fprintln(c.io.errw, c.st.Dim("(press Ctrl-C again, Ctrl-D or /exit to quit)"))
	return false
}

// watchInterrupts cancels the running turn on SIGINT. In raw mode the
// prompt reads Ctrl-C itself, so a signal at the prompt only comes from the
// cooked fallback, where it counts like a Ctrl-C typed at an empty prompt.
func (c *chat) watchInterrupts() {
	for range c.io.interrupts {
		c.mu.Lock()
		cancel := c.cancel
		c.mu.Unlock()
		if cancel != nil {
			fmt.Fprintln(c.io.errw, c.st.Yellow("\ninterrupting the turn…"))
			cancel()
			continue
		}
		if c.promptInterrupt() {
			os.Exit(c.exit())
		}
	}
}

// readMessage reads one message: a line, lines joined by a trailing \, or a
// block between """ lines.
func (c *chat) readMessage() (string, error) {
	line, err := c.ed.ReadLine(c.prompt())
	if err != nil {
		return line, err
	}
	if strings.TrimSpace(line) == `"""` {
		var parts []string
		for {
			l, err := c.ed.ReadLine("... ")
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(l) == `"""` {
				return strings.Join(parts, "\n"), nil
			}
			parts = append(parts, l)
		}
	}
	for strings.HasSuffix(line, `\`) {
		l, err := c.ed.ReadLine("... ")
		if err != nil {
			return "", err
		}
		line = strings.TrimSuffix(line, `\`) + "\n" + l
	}
	return line, nil
}

func (c *chat) prompt() string {
	return "\n" + c.st.Bold(c.st.Glyph("\x1b[36m›\x1b[0m\x1b[1m ", "> "))
}

// turn runs one message as one Agent.Run continuing the conversation.
// asFollowUp is false only for re-running an unfinished checkpoint.
func (c *chat) turn(msg string, asFollowUp bool) {
	s, o, errw := c.s, c.s.o, c.io.errw
	if p := c.playbook; p != nil {
		c.playbook = nil
		fmt.Fprintln(errw, "playbook: "+p.Name+" (/playbook)")
		msg = chatPlaybookMessage(s.root, p, msg, func(m string) { fmt.Fprintln(errw, "warning: "+m) })
	}
	s.turn++
	c.turns++
	prev := s.traj
	cfg := s.turnConfig(prev)
	var r turnResume
	if prev != nil {
		r = turnResume{prev: prev, source: c.source, cont: c.cont}
		if asFollowUp {
			r.followUp = msg
		}
	}
	a, err := s.turnAgent(cfg, nil, r)
	if err != nil {
		// Nothing ran: the conversation stays where it was.
		s.turn--
		c.turns--
		fmt.Fprintln(errw, c.st.Red("error: ")+err.Error())
		fmt.Fprintln(errw, c.st.Dim("nothing was sent; fix that and try again, or /help"))
		return
	}
	act := newChatActivity(errw, c.st)
	termErr, termOut := act.writer(errw), act.writer(c.io.out)
	a.Out, a.Err = lineEndFilter{termErr, &c.md}, termErr
	a.CompactFirst, c.compact = c.compact, false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
	}()
	if s.policy != nil {
		a.PermissionApprover = terminalPermissionApprover
	}
	var events *runEvents
	warn := func(m string) { events.warn(m) }
	db, dbErr := openLogDB(s.fs, *o, cfg, s.root, msg, s.modelName, prev != nil, a, warn)
	tr, trWarn := startOTel(s.otel, cfg, a, msg, prev != nil, o.verbose && s.turn == 1)
	events = startEvents(io.Discard, false, a, prev != nil, cancel, db, tr)
	defer events.close()
	if dbErr != nil {
		warn(dbErr.Error())
	}
	if trWarn != "" {
		warn(trWarn)
	}
	events.emit("chat.turn", map[string]any{"turn": c.turns})
	if s.turn == 1 {
		emitStartTaints(s.lockdown, events)
		startOutputFiles(s.outputFiles, cfg.ToolOutputFiles.MaxRuns, warn)
	}
	if cfg.SkillsOn() {
		loadSkills(a, s.root, pluginSkillSources(cfg), warn)
	}
	s.memoryTurn(a, prev, events, warn, false)
	a.OnStep = func(t *trajectory.Trajectory) error {
		if err := t.Save(c.path); err != nil {
			return err
		}
		checkpointSaved(a, t, c.path, o.planFile)
		return nil
	}
	// Replies render as Markdown a block at a time; tool calls end the
	// block before them and collapse into one line once text resumes.
	var mu sync.Mutex
	streamed, endsLine := false, true
	md := newChatRenderer(termOut, chatWidth(c.io.out), c.outSt)
	summarize := func() {
		if line := act.summary(); line != "" {
			if !endsLine {
				io.WriteString(termOut, "\n")
				endsLine = true
			}
			fmt.Fprintln(termErr, line)
		}
	}
	text := &markerFilter{emit: func(t string) {
		if t == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		summarize()
		streamed = true
		if c.md {
			md.Write(t)
			return
		}
		io.WriteString(termOut, t)
		endsLine = strings.HasSuffix(t, "\n")
	}}
	logged := a.OnEvent
	a.OnEvent = func(kind string, fields map[string]any) {
		if logged != nil {
			logged(kind, fields)
		}
		switch kind {
		case "tool.call":
			text.flush()
			mu.Lock()
			md.Flush()
			mu.Unlock()
			name, _ := fields["name"].(string)
			act.toolCall(name, fields["arguments"])
		case "tool.result":
			isErr, _ := fields["is_error"].(bool)
			act.toolResult(isErr)
		}
	}
	io.WriteString(errw, "\n")
	act.run()
	s.setText(text.write)
	start := time.Now()
	t := a.Run(ctx, msg)
	s.setText(nil)
	text.flush()
	mu.Lock()
	md.Flush()
	summarize()
	mu.Unlock()
	act.finish()
	elapsed := time.Since(start)
	if !streamed && t.Submission != "" && t.ExitStatus != agent.StatusError {
		if c.md {
			md.Write(t.Submission)
			md.Flush()
		} else {
			io.WriteString(c.io.out, strings.TrimRight(t.Submission, "\n")+"\n")
		}
	} else if streamed && !endsLine {
		io.WriteString(c.io.out, "\n")
	}
	if err := t.Save(c.path); err != nil {
		fmt.Fprintln(errw, c.st.Red("chat: checkpoint not saved: "+err.Error()))
	}
	switch t.ExitStatus {
	case agent.StatusSubmitted, agent.StatusCompleted:
	case agent.StatusNeedsInput:
		if q := t.PendingQuestion; q != nil {
			fmt.Fprintln(errw, c.st.Yellow("question: ")+q.Question)
			if len(q.Options) > 0 {
				fmt.Fprintln(errw, "  options: "+strings.Join(q.Options, " | "))
			}
			fmt.Fprintln(errw, c.st.Dim("your next message answers it"))
		}
	case agent.StatusError:
		fmt.Fprintln(errw, c.st.Red("error: ")+t.Submission)
		printHint(errw, s.modelName, t.Submission)
		fmt.Fprintln(errw, c.st.Dim("the conversation is kept; send a message to retry or carry on, or /help"))
	default:
		fmt.Fprintln(errw, c.st.Yellow(t.ExitStatus)+c.st.Dim(" (your next message continues from here)"))
	}
	fmt.Fprintln(errw, "\n"+c.st.Dim(c.turnLine(prev, t, elapsed)))
	events.finish(t, elapsed)
	s.traj, c.source, c.cont = t, "chat session", true
}

// turnLine is the dim line after a turn: its steps, tokens and cost, and
// the session's total cost.
func (c *chat) turnLine(prev, t *trajectory.Trajectory, elapsed time.Duration) string {
	steps, in, out, cost := t.Steps, t.InputTokens, t.OutputTokens, t.Cost
	if prev != nil {
		steps, in, out, cost = steps-prev.Steps, in-prev.InputTokens, out-prev.OutputTokens, cost-prev.Cost
	}
	line := fmt.Sprintf("turn %d · %s · %s in / %s out tokens", c.turns, count(steps, "step"), formatInt(in), formatInt(out))
	if t.Cost > 0 {
		line += fmt.Sprintf(" · %s (session %s)", formatCost(cost), formatCost(t.Cost))
	}
	return line + " · " + formatDuration(elapsed)
}

// exit prints the session summary and how to resume it.
func (c *chat) exit() int {
	t := c.s.traj
	if t == nil {
		fmt.Fprintln(c.io.errw, c.st.Dim("no turns; nothing saved"))
		return 0
	}
	io.WriteString(c.io.errw, c.exitSummary(t))
	return 0
}

// lineEndFilter drops the bare newline the agent writes to end a streamed
// reply when the Markdown renderer already ends its lines.
type lineEndFilter struct {
	w  io.Writer
	md *bool
}

func (f lineEndFilter) Write(p []byte) (int, error) {
	if *f.md && string(p) == "\n" {
		return len(p), nil
	}
	return f.w.Write(p)
}

// exitSummary is the session's totals, where it was saved and how to
// resume it.
func (c *chat) exitSummary(t *trajectory.Trajectory) string {
	st := c.st
	line := fmt.Sprintf("chat ended · %s · %s · %s in / %s out tokens", count(c.turns, "turn"), count(t.Steps, "step"), formatInt(t.InputTokens), formatInt(t.OutputTokens))
	if t.Cost > 0 {
		line += " · " + formatCost(t.Cost)
	}
	line += " · " + formatDuration(time.Since(c.start))
	var b strings.Builder
	b.WriteString("\n" + st.Bold(line) + "\n")
	fmt.Fprintf(&b, "  %s %s\n", st.Dim("saved "), relTo(c.s.root, c.path))
	fmt.Fprintf(&b, "  %s %s\n", st.Dim("resume"), "tinyagent chat --resume "+shellQuote(c.path)+" --cwd "+shellQuote(c.s.root))
	return b.String()
}

// newChatPath returns a fresh .tinyagent/chats/<timestamp>.json under root.
func newChatPath(root string) (string, error) {
	dir := filepath.Join(root, ".tinyagent", "chats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, time.Now().Format("20060102-150405")+".json")
	if _, err := os.Lstat(p); err == nil {
		p = nextOutputPath(p)
	}
	return p, nil
}

// relTo shows path relative to root when it is inside it.
func relTo(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}
