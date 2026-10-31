package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/termmd"
)

// chatActivity is the one status line shown while a turn runs: a spinner
// with what the model is doing ("thinking", or the tool it is running),
// redrawn in place on stderr. It only animates when stderr is styled (a
// terminal with color on); otherwise nothing is drawn in place. Tool calls
// collapse into one summary line when the model writes text again or the
// turn ends.
type chatActivity struct {
	mu      sync.Mutex
	errw    io.Writer
	st      styler
	animate bool
	label   string
	start   time.Time
	frame   int
	shown   bool // the status line is on screen
	midline bool // the last byte written to the terminal wasn't "\n"
	stop    chan struct{}
	done    chan struct{}

	calls  []string // tool names since the last summary
	detail string   // the only call's argument, for a one-call summary
	failed int
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newChatActivity(errw io.Writer, st styler) *chatActivity {
	return &chatActivity{errw: errw, st: st, animate: st.on, label: "thinking", start: time.Now()}
}

// run starts the spinner; finish stops it.
func (a *chatActivity) run() {
	if !a.animate {
		return
	}
	a.stop, a.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(a.done)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-tick.C:
				a.mu.Lock()
				a.draw()
				a.mu.Unlock()
			}
		}
	}()
}

// draw shows the status line; the caller holds a.mu.
func (a *chatActivity) draw() {
	if !a.animate || a.midline {
		return
	}
	a.frame++
	line := spinnerFrames[a.frame%len(spinnerFrames)] + " " + a.label
	if d := time.Since(a.start); d >= 2*time.Second {
		line += " · " + formatDuration(d)
	}
	io.WriteString(a.errw, "\r\x1b[2K"+a.st.Dim(line))
	a.shown = true
}

// clear removes the status line; the caller holds a.mu.
func (a *chatActivity) clear() {
	if a.shown {
		io.WriteString(a.errw, "\r\x1b[2K")
		a.shown = false
	}
}

// writer returns w with the status line cleared before each write.
func (a *chatActivity) writer(w io.Writer) io.Writer { return activityWriter{a, w} }

type activityWriter struct {
	a *chatActivity
	w io.Writer
}

func (aw activityWriter) Write(p []byte) (int, error) {
	aw.a.mu.Lock()
	defer aw.a.mu.Unlock()
	aw.a.clear()
	if len(p) > 0 {
		aw.a.midline = p[len(p)-1] != '\n'
	}
	return aw.w.Write(p)
}

// toolCall notes a tool.call event.
func (a *chatActivity) toolCall(name string, args any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, name)
	a.detail = toolDetail(args)
	a.label = name
	if a.detail != "" {
		a.label += " " + a.detail
	}
	a.start = time.Now()
}

// toolResult notes a tool.result event.
func (a *chatActivity) toolResult(isError bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if isError {
		a.failed++
	}
	a.label, a.start = "thinking", time.Now()
}

// summary returns the collapsed line for the tool calls since the last
// one, and resets them, or "" when there were none.
func (a *chatActivity) summary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) == 0 {
		return ""
	}
	mark := a.st.Glyph("✓", "#")
	var text string
	if len(a.calls) == 1 {
		text = a.calls[0]
		if a.detail != "" {
			text += " " + a.detail
		}
	} else {
		counts := map[string]int{}
		var names []string
		for _, n := range a.calls {
			if counts[n] == 0 {
				names = append(names, n)
			}
			counts[n]++
		}
		var parts []string
		for _, n := range names {
			if counts[n] > 1 {
				n += fmt.Sprintf(" ×%d", counts[n])
			}
			parts = append(parts, n)
		}
		text = fmt.Sprintf("%d tool calls · %s", len(a.calls), strings.Join(parts, ", "))
	}
	if a.failed > 0 {
		mark = a.st.Glyph("✗", "#")
		text += fmt.Sprintf(" · %d failed", a.failed)
	}
	a.calls, a.detail, a.failed = nil, "", 0
	return a.st.Dim("  " + mark + " " + text)
}

// finish stops the spinner and clears its line.
func (a *chatActivity) finish() {
	if a.stop != nil {
		close(a.stop)
		<-a.done
		a.stop = nil
	}
	a.mu.Lock()
	a.clear()
	a.mu.Unlock()
}

// toolDetail picks the argument that says what a call does (a path, a
// command, a pattern), shortened to one line.
func toolDetail(args any) string {
	m, ok := args.(map[string]any)
	if !ok {
		if s, ok := args.(string); ok && json.Unmarshal([]byte(s), &m) != nil {
			return ""
		}
	}
	for _, k := range []string{"path", "command", "pattern", "url", "query", "file", "name"} {
		if v, ok := m[k].(string); ok && v != "" {
			v = strings.Join(strings.Fields(v), " ")
			if utf8.RuneCountInString(v) > 48 {
				v = string([]rune(v)[:47]) + "…"
			}
			return v
		}
	}
	return ""
}

// chatWidth is the width chat wraps replies to: the terminal's, else
// $COLUMNS, else 80, at most 120 so prose stays readable.
func chatWidth(w io.Writer) int {
	n := 0
	if f, ok := w.(*os.File); ok {
		n = terminalWidth(f)
	}
	if n <= 0 {
		n, _ = strconv.Atoi(os.Getenv("COLUMNS"))
	}
	if n <= 0 {
		n = 80
	}
	return min(n, 120)
}

// newChatRenderer returns the Markdown renderer for one turn's reply.
func newChatRenderer(w io.Writer, width int, st styler) *termmd.Renderer {
	return termmd.New(w, termmd.Options{Width: width, Color: st.on, Hyperlinks: st.on && termmd.SupportsHyperlinks(os.Getenv)})
}

// banner is what chat prints when it starts.
func (c *chat) banner() string {
	st := c.st
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", st.Bold("tinyagent "+currentVersion()), st.Dim("· interactive chat"))
	row := func(k, v string) { fmt.Fprintf(&b, "  %s %s\n", st.Dim(fmt.Sprintf("%-6s", k)), v) }
	row("model", c.s.modelName)
	row("cwd", tildePath(c.s.root))
	row("saves", relTo(c.s.root, c.path))
	b.WriteString(st.Dim("Type a message and press Enter. /help lists commands, Ctrl-C stops a turn, Ctrl-D quits.") + "\n")
	return b.String()
}

// tildePath shows a path under the home directory as ~/....
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if r, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(r, "..") && !filepath.IsAbs(r) {
		return "~" + string(filepath.Separator) + r
	}
	return p
}

// count is "1 turn", "2 turns".
func count(n int, one string) string {
	return fmt.Sprintf("%d %s", n, plural(n, one, one+"s"))
}

// helpText is /help.
func (c *chat) helpText() string {
	st := c.st
	var b strings.Builder
	b.WriteString(st.Bold("Commands") + "\n")
	for _, cmd := range chatCommands {
		n := cmd.name
		if cmd.alias != "" {
			n += ", " + cmd.alias
		}
		fmt.Fprintf(&b, "  %-16s %s\n", n, st.Dim(cmd.desc))
	}
	fmt.Fprintf(&b, "  %-16s %s\n", "//text", st.Dim("send a message that starts with /"))
	b.WriteString(st.Bold("Keys") + "\n")
	keys := [][2]string{
		{"Enter", "send"},
		{`\ or """`, "continue on the next line / multi-line block"},
		{"Up, Down", "previous / next message"},
		{"Ctrl-C", "stop the running turn, or clear the line"},
		{"Ctrl-D", "quit"},
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-16s %s\n", k[0], st.Dim(k[1]))
	}
	return b.String()
}

// costText is /cost.
func (c *chat) costText() string {
	t := c.s.traj
	st := c.st
	var b strings.Builder
	row := func(k, v string) { fmt.Fprintf(&b, "  %s %s\n", st.Dim(fmt.Sprintf("%-7s", k)), v) }
	row("turns", strconv.Itoa(c.turns))
	row("steps", strconv.Itoa(t.Steps))
	row("tokens", fmt.Sprintf("%s in / %s out", formatInt(t.InputTokens), formatInt(t.OutputTokens)))
	cost := formatCost(t.Cost)
	if t.Cost == 0 {
		cost = "$0 (no price known for this model, or free)"
	}
	row("cost", cost)
	row("time", formatDuration(time.Since(c.start)))
	return b.String()
}
