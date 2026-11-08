package main

import (
	"fmt"

	"github.com/vicgupta/tinyagent/internal/model"
)

// maxChatInputBytes is how much one chat or web message may hold, so a
// large clipboard paste arrives intact. 8 MiB is several megabytes and
// still fits, after quoting, in the history file's scanner cap.
const maxChatInputBytes = 8 << 20

// largeMessageWarnFraction is the share of the model's context window
// at which a single message is treated as huge. The message is still
// sent; the user is only warned.
const largeMessageWarnFraction = 0.5

// sessionContextWindow is the token window the next chat or web turn
// would use: config context_window, else the built-in table.
func sessionContextWindow(s *session) int {
	if s == nil {
		return 0
	}
	if s.cfg.ContextWindow > 0 {
		return s.cfg.ContextWindow
	}
	return model.ContextWindow(s.modelName)
}

// largeMessageWarning returns a user-facing note when msg is large
// relative to the model's context window, using the same ~4 characters
// per token estimate as /context. An unknown window yields no warning.
func largeMessageWarning(msg string, contextWindow int) string {
	if contextWindow <= 0 || msg == "" {
		return ""
	}
	est := (len(msg) + 3) / 4
	if est < contextWindow && float64(est) < largeMessageWarnFraction*float64(contextWindow) {
		return ""
	}
	if est >= contextWindow {
		return fmt.Sprintf("this message is ~%s tokens, which meets or exceeds the model's %s-token context window; it may fail or trigger compaction", formatInt(est), formatInt(contextWindow))
	}
	pct := float64(est) * 100 / float64(contextWindow)
	return fmt.Sprintf("this message is ~%s tokens (%.0f%% of the model's %s-token context window)", formatInt(est), pct, formatInt(contextWindow))
}

func (c *chat) warnLargeMessage(msg string) {
	if c == nil {
		return
	}
	if note := largeMessageWarning(msg, sessionContextWindow(c.s)); note != "" {
		fmt.Fprintln(c.io.errw, c.st.Yellow("warning: ")+note)
	}
}

func (c *chat) warnTruncatedInput() {
	if c == nil || c.ed == nil || !c.ed.takeTruncated() {
		return
	}
	fmt.Fprintf(c.io.errw, "%sinput was truncated to %s bytes\n", c.st.Yellow("warning: "), formatInt(maxChatInputBytes))
}
