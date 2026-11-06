package main

import (
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestToolsFlagParse(t *testing.T) {
	var f toolsFlag
	if err := f.Set("read_file,grep"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("bash"); err != nil {
		t.Fatal(err)
	}
	if !f.set || f.clear || strings.Join(f.tokens, "|") != "read_file,grep|bash" {
		t.Fatalf("%+v", f)
	}
	var clear toolsFlag
	if err := clear.Set("*"); err != nil || !clear.clear || !clear.set {
		t.Fatalf("clear: %+v err %v", clear, err)
	}
	if err := clear.Set("bash"); err == nil {
		t.Fatal("expected conflict after clear")
	}
}

func TestResolveScopedToolsPrecedence(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	cfg := config.AgentConfig{Tools: []string{"bash"}}
	o := options{}
	got, err := resolveScopedTools(fs, o, &cfg)
	if err != nil || strings.Join(got, ",") != "bash" {
		t.Fatalf("config: %v %v", got, err)
	}
	o.tools = toolsFlag{set: true, tokens: []string{"read_file"}}
	cfg = config.AgentConfig{Tools: []string{"bash"}}
	got, err = resolveScopedTools(fs, o, &cfg)
	if err != nil || strings.Join(got, ",") != "read_file" || strings.Join(cfg.Tools, ",") != "read_file" {
		t.Fatalf("flag overrides config: %v cfg %v err %v", got, cfg.Tools, err)
	}
	o = options{noToolScope: true, tools: toolsFlag{set: true, tokens: []string{"bash"}}}
	if _, err := resolveScopedTools(fs, o, &cfg); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("no-tool-scope vs tools: %v", err)
	}
	o = options{noToolScope: true}
	cfg = config.AgentConfig{Tools: []string{"bash"}}
	got, err = resolveScopedTools(fs, o, &cfg)
	if err != nil || got != nil || cfg.Tools != nil {
		t.Fatalf("no-tool-scope clear: %v %v %v", got, cfg.Tools, err)
	}
	o = options{tools: toolsFlag{set: true, clear: true}}
	cfg = config.AgentConfig{Tools: []string{"bash"}}
	got, err = resolveScopedTools(fs, o, &cfg)
	if err != nil || got != nil || cfg.Tools != nil {
		t.Fatalf("tools=* clear: %v %v %v", got, cfg.Tools, err)
	}
}

func TestValidateRunToolsUnknownAndReadOnly(t *testing.T) {
	e := &tools.Executor{Root: t.TempDir(), Only: []string{"nope"}}
	if err := validateRunTools(e); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown: %v", err)
	}
	e = &tools.Executor{Root: t.TempDir(), ReadOnly: true, Only: []string{"write_file"}}
	if err := validateRunTools(e); err == nil || !strings.Contains(err.Error(), "write_file") {
		t.Fatalf("read-only conflict: %v", err)
	}
	e = &tools.Executor{Root: t.TempDir(), WebSearch: &tools.WebSearcher{}, Only: []string{"read_file", "web_search"}}
	if err := validateRunTools(e); err != nil {
		t.Fatal(err)
	}
}

func TestCLIToolsRestrictsAndRejects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "fake")
	code, stdout, stderr := runCLI(t, "--check", "-m", "openai/gpt-4o", "--cwd", dir)
	if code != 0 {
		t.Fatalf("check default: %d %s", code, stderr)
	}
	out := stdout + stderr
	if strings.Contains(out, "tool_scope:") {
		t.Fatalf("default should not scope: %s", out)
	}

	code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--tools", "nonexistent", "--step-limit", "1")
	if code != 2 || !strings.Contains(stderr, "nonexistent") {
		t.Fatalf("unknown tool: exit %d %s", code, stderr)
	}
	code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--read-only", "--tools", "write_file", "--step-limit", "1")
	if code != 2 || !strings.Contains(stderr, "write_file") {
		t.Fatalf("read-only conflict: exit %d %s", code, stderr)
	}

	cfg := writeConfig(t, "tools: [read_file, grep]\n")
	code, stdout, stderr = runCLI(t, "--check", "-m", "openai/gpt-4o", "--cwd", dir, "-c", cfg)
	out = stdout + stderr
	if code != 0 || !strings.Contains(out, "tool_scope:") || !strings.Contains(out, "read_file") {
		t.Fatalf("config tools check: %d %s", code, out)
	}
	code, stdout, stderr = runCLI(t, "--check", "-m", "openai/gpt-4o", "--cwd", dir, "-c", cfg, "--no-tool-scope")
	out = stdout + stderr
	if code != 0 || strings.Contains(out, "tool_scope:") {
		t.Fatalf("no-tool-scope clears: %d %s", code, out)
	}
}

func TestResumeScopedToolsConflict(t *testing.T) {
	src := &trajectory.Trajectory{ScopedTools: []string{"read_file", "grep"}}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	o := options{tools: toolsFlag{set: true, tokens: []string{"bash"}}}
	err := checkResumeScopedTools(fs, o, src)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("got %v", err)
	}
	o = options{tools: toolsFlag{set: true, tokens: []string{"read_file,grep"}}}
	if err := checkResumeScopedTools(fs, o, src); err != nil {
		t.Fatal(err)
	}
	o = options{noToolScope: true}
	if err := checkResumeScopedTools(fs, o, src); err == nil {
		t.Fatal("expected no-tool-scope conflict")
	}
	empty := &trajectory.Trajectory{}
	if err := checkResumeScopedTools(fs, options{}, empty); err != nil {
		t.Fatal(err)
	}
}

func TestNewRunExecutorHonorsConfigTools(t *testing.T) {
	e := newRunExecutor(config.AgentConfig{Tools: []string{"read_file", "grep"}}, toolWiring{root: t.TempDir()})
	if err := validateRunTools(e); err != nil {
		t.Fatal(err)
	}
	got := e.Catalog()
	var ns []string
	for _, s := range got {
		ns = append(ns, s.Function.Name)
	}
	if strings.Join(ns, ",") != "read_file,grep" {
		t.Fatalf("catalog = %v", ns)
	}
	res := e.RunResult(context.Background(), model.ToolCall{ID: "1", Function: model.FunctionCall{Name: "bash", Arguments: `{"command":"echo hi"}`}})
	if !res.IsError || !strings.Contains(res.Text, "not available in this run") {
		t.Fatalf("bash: %+v", res)
	}
}
