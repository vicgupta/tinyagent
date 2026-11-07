package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func usageAgent(t *testing.T, window int, only ...string) *Agent {
	t.Helper()
	return &Agent{
		ModelName:    "openai/gpt-4o",
		Config:       config.AgentConfig{ContextWindow: window, SystemPrompt: "system prompt text!!"},
		Tools:        &tools.Executor{Root: t.TempDir(), Only: only},
		SkipAgentsMD: true,
	}
}

func TestContextUsageEstimateAndSections(t *testing.T) {
	a := usageAgent(t, 10_000, "no-such-tool")
	agents := "\n\n## Project instructions (AGENTS.md)\n\nrules!"
	catalog := "\n\n## Skills\n\nThese skills hold instructions for specific tasks. Load one.\n- release: Cut a release"
	notice := "\n\n" + readOnlyNotice
	sys := "base!!" + agents + catalog + notice
	big := strings.Repeat("x", 400)
	small := strings.Repeat("y", 40)
	tr := &trajectory.Trajectory{Messages: []model.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"seq 1 3000"}`), call("2", "read_file", `{"path":"main.go"}`)}},
		{Role: "tool", ToolCallID: "1", Content: big},
		{Role: "tool", ToolCallID: "2", Content: small},
		{Role: "tool", ToolCallID: "3", Content: skills.ContentPrefix + "release\">\nbody of the skill"},
		{Role: "assistant", Content: "done"},
	}}
	// A summary replaces nothing here; add one beside the messages via a view.
	tr.Compactions = []trajectory.Compaction{{
		Step:    0,
		Summary: "abcd",
		View:    []int{0, 1, -1, 2, 3, 4, 5, 6},
	}}

	u := a.ContextUsage(tr, nil)
	if u.Measured || u.Window != 10_000 || u.Threshold != 8_000 {
		t.Fatalf("header = measured %v window %d threshold %d", u.Measured, u.Window, u.Threshold)
	}
	wantAgents := len(agents) / 4
	if u.Agents != wantAgents {
		t.Errorf("AGENTS.md tokens = %d, want %d", u.Agents, wantAgents)
	}
	if u.Skills == 0 || len(u.SkillNames) != 1 || u.SkillNames[0] != "release" {
		t.Errorf("skills = %d %v", u.Skills, u.SkillNames)
	}
	if u.System == 0 || strings.Contains(sys, readOnlyNotice) && u.System < len(notice)/4 {
		t.Errorf("system tokens = %d", u.System)
	}
	summary := len(summaryHeader+"abcd") / 4
	if u.Summary != summary {
		t.Errorf("summary = %d, want %d", u.Summary, summary)
	}
	if u.User != len("task")/4 {
		t.Errorf("user = %d, want %d (summary must not be counted as a user message)", u.User, len("task")/4)
	}
	asst := msgChars(tr.Messages[2])/4 + len("done")/4
	if u.Assistant != asst {
		t.Errorf("assistant = %d, want %d", u.Assistant, asst)
	}
	if u.ToolResults != len(big)/4+len(small)/4 {
		t.Errorf("tool results = %d, want %d (skill load must not be included)", u.ToolResults, len(big)/4+len(small)/4)
	}
	if len(u.Largest) != 2 || u.Largest[0].Label != "bash seq 1 3000" || u.Largest[0].Tokens != len(big)/4 || u.Largest[1].Label != "read_file main.go" {
		t.Errorf("largest = %+v", u.Largest)
	}
	if u.Messages() != u.User+u.Assistant+u.ToolResults {
		t.Errorf("messages = %d", u.Messages())
	}
	parts := u.System + u.Agents + u.Memory + u.Rules + u.Skills + u.Tools + u.User + u.Assistant + u.ToolResults + u.Summary + u.Plan
	if u.Used != parts {
		t.Errorf("used %d != parts %d", u.Used, parts)
	}
	if u.Pending != 0 {
		t.Errorf("pending = %d", u.Pending)
	}
}

func TestContextUsageScalesToReportedUsage(t *testing.T) {
	a := usageAgent(t, 1_000, "no-such-tool")
	tr := &trajectory.Trajectory{
		Messages: []model.Message{
			{Role: "system", Content: strings.Repeat("a", 16)},
			{Role: "user", Content: strings.Repeat("b", 8)},
			{Role: "assistant", Content: strings.Repeat("c", 4)},
		},
		LastUsage: &trajectory.LastUsage{InputTokens: 80, OutputTokens: 20, Messages: 3, Step: 1},
	}
	u := a.ContextUsage(tr, nil)
	if !u.Measured || u.ReportedIn != 80 || u.ReportedOut != 20 || u.Pending != 0 {
		t.Fatalf("measured = %+v", u)
	}
	// 4, 2 and 1 raw tokens scaled to 100 by largest remainder:
	// 57 + 29 + 14.
	if u.System != 57 || u.User != 29 || u.Assistant != 14 || u.Used != 100 {
		t.Fatalf("scaled = system %d user %d assistant %d used %d", u.System, u.User, u.Assistant, u.Used)
	}

	tr.Messages = append(tr.Messages, model.Message{Role: "tool", ToolCallID: "1", Content: strings.Repeat("d", 8)})
	u = a.ContextUsage(tr, nil)
	if u.System != 57 || u.User != 29 || u.Assistant != 14 || u.ToolResults != 2 || u.Pending != 2 || u.Used != 102 {
		t.Fatalf("pending = system %d user %d assistant %d tools %d pending %d used %d", u.System, u.User, u.Assistant, u.ToolResults, u.Pending, u.Used)
	}
}

func TestContextUsageIgnoresStaleReport(t *testing.T) {
	a := usageAgent(t, 1_000, "no-such-tool")
	tr := &trajectory.Trajectory{
		Messages: []model.Message{
			{Role: "system", Content: strings.Repeat("a", 16)},
			{Role: "user", Content: strings.Repeat("b", 8)},
			{Role: "assistant", Content: strings.Repeat("c", 4)},
		},
		LastUsage:   &trajectory.LastUsage{InputTokens: 80, OutputTokens: 20, Messages: 3, Step: 1},
		Compactions: []trajectory.Compaction{{Step: 1, View: []int{0, 1, 2}}},
	}
	u := a.ContextUsage(tr, nil)
	if u.Measured || u.Used != 7 {
		t.Fatalf("stale report applied: measured %v used %d", u.Measured, u.Used)
	}
}

func TestContextUsageToolDefinitions(t *testing.T) {
	a := usageAgent(t, 128_000, "read_file", "docs__echo")
	a.Tools.ExternalSpecs = []model.ToolSpec{{
		Type: "function",
		Function: model.FunctionSpec{
			Name:        "docs__echo",
			Description: "Echo from the docs server",
			Parameters:  map[string]any{"type": "object"},
		},
	}}
	u := a.ContextUsage(nil, map[string]string{"docs__echo": "mcp"})
	specs := a.Tools.Catalog()
	if len(specs) != 2 {
		t.Fatalf("catalog = %v", toolNames(specs))
	}
	want := 0
	var echo int
	for _, spec := range specs {
		b, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		n := len(b) / 4
		want += n
		if spec.Function.Name == "docs__echo" {
			echo = n
		}
	}
	if u.Tools != want {
		t.Errorf("tool tokens = %d, want %d", u.Tools, want)
	}
	var found bool
	for _, it := range u.ToolItems {
		if it.Label == "mcp docs__echo" && it.Tokens == echo {
			found = true
		}
	}
	if !found {
		t.Errorf("tool items = %+v", u.ToolItems)
	}
	if u.Measured || u.System == 0 || u.Messages() != 0 {
		t.Errorf("preview = measured %v system %d messages %d", u.Measured, u.System, u.Messages())
	}
	parts := u.System + u.Tools
	if u.Used != parts {
		t.Errorf("used %d != system+tools %d", u.Used, parts)
	}
}

func TestContextUsageImageUsesEstimator(t *testing.T) {
	a := usageAgent(t, 100_000, "no-such-tool")
	tr := &trajectory.Trajectory{Messages: []model.Message{
		{Role: "system", Content: "sys!"},
		{Role: "user", Content: "see", Images: []model.Image{{MIMEType: "image/png", Data: "aaaa"}}},
	}}
	u := a.ContextUsage(tr, nil)
	want := msgChars(tr.Messages[1]) / 4
	if u.User != want || want != len("see")/4+model.ImageTokenEstimate {
		t.Fatalf("user = %d, msgChars/4 = %d", u.User, want)
	}
}

func TestContextUsageHandoffReportsProviderTotal(t *testing.T) {
	a := usageAgent(t, 200_000, "read_file")
	a.ModelName = "claude-code/claude-sonnet-4-5"
	tr := &trajectory.Trajectory{
		Messages: []model.Message{
			{Role: "system", Content: strings.Repeat("a", 400)},
			{Role: "assistant", Content: "done"},
		},
		LastUsage: &trajectory.LastUsage{InputTokens: 1000, OutputTokens: 50, Messages: 2, Step: 1},
	}
	u := a.ContextUsage(tr, nil)
	if !u.Handoff || !u.Measured || u.Used != 1050 || u.System != 0 || u.Tools != 0 || u.Window != 200_000 || u.Threshold != 160_000 {
		t.Fatalf("%+v", u)
	}
	tr.LastUsage = nil
	u = a.ContextUsage(tr, nil)
	if !u.Handoff || u.Measured || u.Used != 0 {
		t.Fatalf("unmeasured handoff = %+v", u)
	}
}

func TestContextUsageUnknownWindow(t *testing.T) {
	a := usageAgent(t, 0, "no-such-tool")
	a.ModelName = "custom/not-a-known-model"
	u := a.ContextUsage(nil, nil)
	if u.Window != 0 || u.Threshold != 0 || u.Used == 0 || u.Measured {
		t.Fatalf("%+v", u)
	}
}
