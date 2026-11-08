package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
)

// chatCommands are the slash commands, in /help order.
var chatCommands = []struct{ name, alias, desc string }{
	{"/help", "", "show this list"},
	{"/exit", "/quit", "end the session (also Ctrl-D)"},
	{"/cost", "", "tokens and cost so far"},
	{"/context", "", "show how the context window is used"},
	{"/tools", "", "list tools available for this session"},
	{"/new", "/clear", "start a fresh conversation (new checkpoint)"},
	{"/save", "", "save the checkpoint now, or to /save <path>"},
	{"/compact", "", "compact the conversation now and show the size before and after"},
	{"/raw", "", "toggle showing replies as raw Markdown"},
	{"/model", "", "show or switch the model: /model <provider/model>"},
}

// command runs one slash command and reports whether the session ends.
func (c *chat) command(line string) bool {
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	w := c.io.errw
	switch name {
	case "/help":
		fmt.Fprint(w, c.helpText())
	case "/exit", "/quit":
		return true
	case "/tools":
		if t := c.s.traj; t != nil && len(t.ScopedTools) > 0 {
			fmt.Fprintln(w, "tools: "+strings.Join(t.ScopedTools, ", ")+" (scoped)")
			return false
		}
		if len(c.s.cfg.Tools) > 0 {
			fmt.Fprintln(w, "tools: "+strings.Join(c.s.cfg.Tools, ", ")+" (scoped)")
			return false
		}
		if t := c.s.traj; t != nil && len(t.Tools) > 0 {
			fmt.Fprintln(w, "tools: "+strings.Join(t.Tools, ", "))
			return false
		}
		fmt.Fprintln(w, "tools: all enabled tools (no --tools scope)")
	case "/cost":
		t := c.s.traj
		if t == nil {
			fmt.Fprintln(w, "no turns yet")
			return false
		}
		fmt.Fprint(w, c.costText())
	case "/context":
		fmt.Fprint(w, formatContext(c.s.contextReport(), c.st))
	case "/model":
		if arg == "" {
			fmt.Fprint(w, modelStatusText(c.s, c.st))
			return false
		}
		if err := c.s.switchModel(arg, w); err != nil {
			msg := err.Error()
			if h := model.Hint(arg, msg); h != "" {
				msg += " (" + h + ")"
			}
			fmt.Fprintln(w, c.st.Red("error: "+msg))
			return false
		}
		fmt.Fprintln(w, c.st.Dim("model: "+arg))
	case "/new", "/clear":
		c.s.traj, c.source, c.cont, c.turns = nil, "", false, 0
		c.s.remember = nil
		if c.s.o.output != "" {
			c.path = nextOutputPath(c.path)
		} else if p, err := newChatPath(c.s.root); err == nil {
			c.path = p
		} else {
			fmt.Fprintln(w, c.st.Red("error: "+err.Error()))
			return false
		}
		fmt.Fprintln(w, c.st.Dim("new conversation · checkpoint "+relTo(c.s.root, c.path)))
	case "/save":
		t := c.s.traj
		if t == nil {
			fmt.Fprintln(w, "nothing to save yet")
			return false
		}
		path := c.path
		if arg != "" {
			path = arg
		}
		if err := t.Save(path); err != nil {
			fmt.Fprintln(w, c.st.Red("error: "+err.Error()))
			return false
		}
		fmt.Fprintln(w, "saved "+path)
	case "/compact":
		ctx, cancel := context.WithCancel(context.Background())
		c.mu.Lock()
		c.cancel = cancel
		c.mu.Unlock()
		err := c.s.compactNow(ctx, w, c.path)
		cancel()
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
		if err != nil {
			fmt.Fprintln(w, c.st.Red("error: "+err.Error()))
			fmt.Fprintln(w, c.st.Dim("history was left unchanged; try again, or /help"))
		}
	case "/raw":
		c.md = !c.md
		if c.md {
			fmt.Fprintln(w, c.st.Dim("replies render as Markdown again"))
		} else {
			fmt.Fprintln(w, c.st.Dim("replies show as raw Markdown (/raw again to render)"))
		}
	default:
		var names []string
		for _, cmd := range chatCommands {
			names = append(names, strings.TrimPrefix(cmd.name, "/"))
			if cmd.alias != "" {
				names = append(names, strings.TrimPrefix(cmd.alias, "/"))
			}
		}
		msg := "unknown command " + name
		if s := suggestFlags(strings.TrimPrefix(name, "/"), names); len(s) > 0 {
			msg += "; did you mean /" + s[0] + "?"
		}
		fmt.Fprintln(w, msg+" (/help lists them; //text sends a message starting with /)")
	}
	return false
}
