package main

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

func TestFormatContextMeasured(t *testing.T) {
	u := agent.ContextUsage{
		Model: "openai/gpt-4o", Window: 1000, Used: 250, Threshold: 800,
		System: 100, Agents: 40, Tools: 30,
		ToolItems: []agent.ContextItem{{Label: "read_file", Tokens: 20}, {Label: "mcp docs__echo", Tokens: 10}},
		User:      50, Assistant: 20, ToolResults: 10,
		Largest:     []agent.ContextItem{{Label: "bash seq 1 3000", Tokens: 10}},
		Measured:    true,
		ReportedIn:  200,
		ReportedOut: 40,
		Pending:     10,
	}
	got := formatContext(u, styler{})
	want := strings.Join([]string{
		"context  openai/gpt-4o · 1,000 token window",
		"[#####---------------] 25.0% · 250 / 1,000  (last turn + estimate)",
		"  system prompt                100   10.0%",
		"  AGENTS.md                     40    4.0%",
		"  tools                         30    3.0%",
		"    read_file                   20",
		"    mcp docs__echo              10",
		"  messages                      80    8.0%",
		"    user                        50",
		"    assistant                   20",
		"    tool results                10",
		"      bash seq 1 3000           10",
		"  free                         750   75.0%",
		"  until auto-compact           550  threshold 800 (80%)",
		"sizes are estimates, scaled to the last turn's reported usage (200 in + 40 out); 10 tokens added since are not scaled",
		"",
	}, "\n")
	if got != want {
		t.Errorf("format =\n%s\nwant =\n%s", got, want)
	}
}

func TestFormatContextEstimatedAndEdges(t *testing.T) {
	est := formatContext(agent.ContextUsage{
		Model: "openai/gpt-4o", Window: 1000, Used: 100, Threshold: 800, System: 100,
	}, styler{})
	if !strings.Contains(est, "[##------------------] 10.0% · ~100 / 1,000  (estimated)") || !strings.Contains(est, "sizes are estimates (~4 characters per token)") {
		t.Errorf("estimate =\n%s", est)
	}
	if !strings.Contains(est, "  until auto-compact           700  threshold 800 (80%)") {
		t.Errorf("threshold line missing:\n%s", est)
	}

	off := formatContext(agent.ContextUsage{
		Model: "openai/gpt-4o", Window: 1000, NoCompact: true, System: 10, Used: 10, Threshold: 800,
	}, styler{})
	if !strings.Contains(off, "auto-compact off (--no-compact)") || strings.Contains(off, "until auto-compact") {
		t.Errorf("no-compact =\n%s", off)
	}

	over := formatContext(agent.ContextUsage{
		Model: "openai/gpt-4o", Window: 1000, Used: 1100, Threshold: 800, System: 1100, Measured: true,
	}, styler{})
	if !strings.Contains(over, "[####################] 110.0% · 1,100 / 1,000  (last turn)") || !strings.Contains(over, "over the window by 100") || !strings.Contains(over, "past auto-compact") {
		t.Errorf("over =\n%s", over)
	}

	tiny := formatContext(agent.ContextUsage{Model: "openai/gpt-4o", Window: 128000, Used: 1, Threshold: 102400, System: 1}, styler{})
	if !strings.Contains(tiny, "[#-------------------] <0.1%") || !strings.Contains(tiny, "system prompt                  1   <0.1%") {
		t.Errorf("tiny percent =\n%s", tiny)
	}

	unknown := formatContext(agent.ContextUsage{Model: "custom/x", Used: 40, System: 40}, styler{})
	if !strings.Contains(unknown, "context  custom/x · context window unknown") || !strings.Contains(unknown, "~40 tokens (estimated)") || !strings.Contains(unknown, "need a known context window") {
		t.Errorf("unknown =\n%s", unknown)
	}

	hand := formatContext(agent.ContextUsage{
		Model: "claude-code/claude-sonnet-4-5", Window: 200000, Used: 1050, Threshold: 160000,
		Handoff: true, Measured: true, ReportedIn: 1000, ReportedOut: 50,
	}, styler{})
	for _, want := range []string{
		"claude-code/claude-sonnet-4-5 · 200,000 token window",
		"(last turn)",
		"the CLI owns this context",
		"1,000 in + 50 out",
		"free",
		"until auto-compact",
	} {
		if !strings.Contains(hand, want) {
			t.Errorf("handoff missing %q\n%s", want, hand)
		}
	}
	if strings.Contains(hand, "\n  system prompt") {
		t.Errorf("handoff broke down TinyAgent's prompt:\n%s", hand)
	}

	items := make([]agent.ContextItem, 10)
	for i := range items {
		items[i] = agent.ContextItem{Label: "tool", Tokens: 10 - i}
	}
	items[7].Label = "mcp docs__echo"
	got := visibleTools(items)
	if len(got) != 6 || got[5].Label != "5 more, including mcp/plugin" || got[5].Tokens != 15 {
		t.Errorf("grouped = %+v", got)
	}
}
