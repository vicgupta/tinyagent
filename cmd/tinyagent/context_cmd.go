package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// contextReport is the context window the next chat turn would send.
func (s *session) contextReport() agent.ContextUsage {
	e := newRunExecutor(s.cfg, toolWiring{
		root:        s.root,
		env:         s.env,
		cmdTools:    s.cmdTools,
		extSpecs:    s.extSpecs,
		extCall:     s.extCall,
		mcp:         s.mcp,
		subagents:   s.subagents,
		workers:     s.workers,
		webFetch:    s.webFetch,
		webSearch:   s.webSearch,
		outputFiles: s.outputFiles,
		fileAccess:  s.fileAccess,
		extraDirs:   s.extraDirs,
		redactor:    s.redactor,
	})
	// Advertise lsp when it is enabled without starting the servers.
	// A turn builds a real client; the definition is the same either way.
	if s.cfg.LSP.Enabled && e.LSP == nil {
		e.LSP = &tools.LSP{}
	}
	a := &agent.Agent{
		Model:          s.model,
		ModelName:      s.modelName,
		FallbackModels: s.fallbacks,
		Config:         s.cfg,
		Tools:          e,
		SkipAgentsMD:   s.o.noAgentsMD,
		NoCompact:      s.o.noCompact,
		Out:            io.Discard,
		Err:            io.Discard,
	}
	if s.cfg.SkillsOn() {
		loadSkills(a, s.root, pluginSkillSources(s.cfg), func(string) {})
	}
	if s.traj == nil && s.cfg.Memory.Enabled {
		loadMemory(a, s.cfg.Memory, s.root, func(string) {})
		if w := memoryWriter(a, s.cfg.Memory); w != nil {
			a.Tools.Remember = w
			a.Tools.MemoryTools = true
		}
	} else if s.remember != nil {
		a.Tools.Remember = s.remember
		a.Tools.MemoryTools = true
	}
	return a.ContextUsage(s.traj, s.toolOrigins())
}

func (s *session) toolOrigins() map[string]string {
	origin := map[string]string{}
	if s.mcp != nil {
		for _, sp := range s.mcp.Specs {
			origin[sp.Function.Name] = "mcp"
		}
	}
	for _, sp := range s.extSpecs {
		if _, ok := origin[sp.Function.Name]; !ok {
			origin[sp.Function.Name] = "plugin"
		}
	}
	return origin
}

// formatContext is the /context report. st styles labels; with styling off
// the text is plain ASCII, including the bar.
func formatContext(u agent.ContextUsage, st styler) string {
	var b strings.Builder
	modelName := u.Model
	if modelName == "" {
		modelName = "unknown model"
	}
	if u.Window > 0 {
		fmt.Fprintf(&b, "context  %s · %s token window\n", modelName, formatInt(u.Window))
		fmt.Fprintf(&b, "%s %s · %s / %s  (%s)\n", contextBar(u.Used, u.Window), contextPct(u.Used, u.Window, false), contextTotal(u), formatInt(u.Window), contextKind(u))
	} else {
		fmt.Fprintf(&b, "context  %s · context window unknown\n", modelName)
		fmt.Fprintf(&b, "%s tokens (%s)\n", contextTotal(u), contextKind(u))
	}
	if u.Handoff {
		b.WriteString(st.Dim("handoff: the CLI owns this context, so TinyAgent cannot break down the system prompt, tools or messages.") + "\n")
		if u.Measured {
			fmt.Fprintf(&b, "%s\n", st.Dim(fmt.Sprintf("total is the last turn's reported usage (%s in + %s out).", formatInt(u.ReportedIn), formatInt(u.ReportedOut))))
		} else {
			b.WriteString(st.Dim("no usage has been reported for the last turn.") + "\n")
		}
	} else {
		writeContextRow(&b, st, "  ", "system prompt", u.System, u.Window, "")
		if u.Agents > 0 {
			writeContextRow(&b, st, "  ", "AGENTS.md", u.Agents, u.Window, "")
		}
		if u.Memory > 0 {
			writeContextRow(&b, st, "  ", "memory", u.Memory, u.Window, "")
		}
		if u.Rules > 0 {
			writeContextRow(&b, st, "  ", "project rules", u.Rules, u.Window, "")
		}
		if u.Skills > 0 {
			extra := ""
			if len(u.SkillNames) > 0 {
				names := u.SkillNames
				if len(names) > 4 {
					names = append(append([]string{}, names[:4]...), "…")
				}
				extra = strings.Join(names, ", ")
			}
			writeContextRow(&b, st, "  ", "skills", u.Skills, u.Window, extra)
		}
		if u.Plan > 0 {
			writeContextRow(&b, st, "  ", "plan", u.Plan, u.Window, "")
		}
		writeContextRow(&b, st, "  ", "tools", u.Tools, u.Window, "")
		for _, it := range visibleTools(u.ToolItems) {
			writeContextRow(&b, st, "    ", it.Label, it.Tokens, 0, "")
		}
		writeContextRow(&b, st, "  ", "messages", u.Messages(), u.Window, "")
		if u.User > 0 {
			writeContextRow(&b, st, "    ", "user", u.User, 0, "")
		}
		if u.Assistant > 0 {
			writeContextRow(&b, st, "    ", "assistant", u.Assistant, 0, "")
		}
		if u.ToolResults > 0 {
			writeContextRow(&b, st, "    ", "tool results", u.ToolResults, 0, "")
			for _, it := range u.Largest {
				writeContextRow(&b, st, "      ", it.Label, it.Tokens, 0, "")
			}
		}
		if u.Summary > 0 {
			writeContextRow(&b, st, "  ", "compaction summary", u.Summary, u.Window, "")
		}
	}
	if u.Window > 0 {
		free := u.Window - u.Used
		if free < 0 {
			free = 0
		}
		writeContextRow(&b, st, "  ", "free", free, u.Window, "")
		if u.Used > u.Window {
			fmt.Fprintf(&b, "%s\n", st.Dim(fmt.Sprintf("  over the window by %s", formatInt(u.Used-u.Window))))
		}
		switch {
		case u.NoCompact:
			b.WriteString(st.Dim("  auto-compact off (--no-compact)") + "\n")
		default:
			dist := u.Threshold - u.Used
			label := "until auto-compact"
			extra := fmt.Sprintf("threshold %s (80%%)", formatInt(u.Threshold))
			if dist < 0 {
				label = "past auto-compact"
				dist = -dist
			}
			writeContextRow(&b, st, "  ", label, dist, 0, extra)
		}
	} else {
		b.WriteString(st.Dim("free space and the auto-compact threshold need a known context window") + "\n")
	}
	switch {
	case u.Handoff:
	case u.Measured && u.Pending > 0:
		fmt.Fprintf(&b, "%s\n", st.Dim(fmt.Sprintf("sizes are estimates, scaled to the last turn's reported usage (%s in + %s out); %s tokens added since are not scaled", formatInt(u.ReportedIn), formatInt(u.ReportedOut), formatInt(u.Pending))))
	case u.Measured:
		fmt.Fprintf(&b, "%s\n", st.Dim(fmt.Sprintf("sizes are estimates, scaled to the last turn's reported usage (%s in + %s out)", formatInt(u.ReportedIn), formatInt(u.ReportedOut))))
	default:
		b.WriteString(st.Dim("sizes are estimates (~4 characters per token)") + "\n")
	}
	return b.String()
}

func contextKind(u agent.ContextUsage) string {
	switch {
	case u.Measured && u.Pending > 0:
		return "last turn + estimate"
	case u.Measured:
		return "last turn"
	default:
		return "estimated"
	}
}

func contextTotal(u agent.ContextUsage) string {
	n := formatInt(u.Used)
	if !u.Measured {
		return "~" + n
	}
	return n
}

// contextPct formats part/window. width pads to the row column. A non-zero
// share that would show as 0.0% is "<0.1%" so a small piece is not invisible.
func contextPct(part, window int, width bool) string {
	if window <= 0 {
		return ""
	}
	p := 100 * float64(part) / float64(window)
	if part > 0 && p < 0.05 {
		if width {
			return " <0.1%"
		}
		return "<0.1%"
	}
	if width {
		return fmt.Sprintf("%5.1f%%", p)
	}
	return fmt.Sprintf("%.1f%%", p)
}

func contextBar(used, window int) string {
	const n = 20
	filled := 0
	if window > 0 && used > 0 {
		filled = used * n / window
		if filled < 1 {
			filled = 1
		}
		if filled > n {
			filled = n
		}
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", n-filled) + "]"
}

func writeContextRow(b *strings.Builder, st styler, indent, label string, tokens, window int, extra string) {
	lab := indent + label
	if len(lab) < 24 {
		lab += strings.Repeat(" ", 24-len(lab))
	}
	fmt.Fprintf(b, "%s%10s", st.Dim(lab), formatInt(tokens))
	if window > 0 {
		fmt.Fprintf(b, "  %s", contextPct(tokens, window, true))
	}
	if extra != "" {
		fmt.Fprintf(b, "  %s", st.Dim(extra))
	}
	b.WriteByte('\n')
}

// visibleTools lists every tool, or the largest few plus a remainder line
// when the catalog is long. MCP and plugin tools keep their group prefix.
func visibleTools(items []agent.ContextItem) []agent.ContextItem {
	if len(items) <= 8 {
		return items
	}
	out := append([]agent.ContextItem{}, items[:5]...)
	rest, external := 0, false
	for _, it := range items[5:] {
		rest += it.Tokens
		if strings.HasPrefix(it.Label, "mcp ") || strings.HasPrefix(it.Label, "plugin ") || strings.HasPrefix(it.Label, "external ") {
			external = true
		}
	}
	label := fmt.Sprintf("%d more", len(items)-5)
	if external {
		label = fmt.Sprintf("%d more, including mcp/plugin", len(items)-5)
	}
	return append(out, agent.ContextItem{Label: label, Tokens: rest})
}
