package main

import (
	"fmt"
	"strings"
)

// chatCommands are the slash commands, in /help order.
var chatCommands = []struct{ name, alias, desc string }{
	{"/help", "", "show this list"},
	{"/exit", "/quit", "end the session (also Ctrl-D)"},
	{"/cost", "", "tokens and cost so far"},
	{"/new", "/clear", "start a fresh conversation (new checkpoint)"},
	{"/save", "", "save the checkpoint now, or to /save <path>"},
	{"/compact", "", "compact the conversation before the next request"},
	{"/raw", "", "toggle showing replies as raw Markdown"},
	{"/playbook", "", "follow playbook <name> from ./playbooks for the next message"},
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
	case "/cost":
		t := c.s.traj
		if t == nil {
			fmt.Fprintln(w, "no turns yet")
			return false
		}
		fmt.Fprint(w, c.costText())
	case "/new", "/clear":
		c.s.traj, c.source, c.cont, c.turns, c.compact = nil, "", false, 0, false
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
		if c.s.traj == nil {
			fmt.Fprintln(w, "nothing to compact yet")
			return false
		}
		if c.s.o.noCompact {
			fmt.Fprintln(w, "compaction is off (--no-compact)")
			return false
		}
		c.compact = true
		fmt.Fprintln(w, c.st.Dim("the next message compacts the conversation first"))
	case "/raw":
		c.md = !c.md
		if c.md {
			fmt.Fprintln(w, c.st.Dim("replies render as Markdown again"))
		} else {
			fmt.Fprintln(w, c.st.Dim("replies show as raw Markdown (/raw again to render)"))
		}
	case "/playbook":
		if arg == "" {
			fmt.Fprintln(w, "usage: /playbook <name> (tinyagent playbooks list shows them)")
			return false
		}
		p, err := chatPlaybook(c.s.root, arg)
		if err != nil {
			fmt.Fprintln(w, c.st.Red("error: "+err.Error()))
			return false
		}
		c.playbook = p
		fmt.Fprintln(w, c.st.Dim("the next message follows playbook "+p.Name))
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
