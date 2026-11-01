package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

func TestNewRunExecutorCopiesConfig(t *testing.T) {
	var cfg config.AgentConfig
	cfg.ReadOnly = true
	cfg.BashTimeoutSec = 7
	cfg.MaxOutputChars = 1234
	cfg.MaxBackgroundProcesses = 3
	cfg.Plan.Enabled = true
	cfg.Images.Enabled = true
	cfg.ApplyPatch = true
	cfg.AskUser.Enabled = true
	cfg.MaxImageBytes = 99

	e := newRunExecutor(cfg, toolWiring{root: "/r"})
	if e.Root != "/r" || !e.ReadOnly || e.BashTimeout != 7*time.Second ||
		e.MaxOutputChars != 1234 || e.MaxProcesses != 3 || !e.Plan || !e.Images ||
		!e.ApplyPatch || !e.AskUser || e.MaxImageBytes != 99 {
		t.Fatalf("config not copied: %+v", e)
	}
	if e.Delegate || e.Workers || e.TaskTypes != nil {
		t.Fatalf("delegation on without subagents or workers: %+v", e)
	}
	if e.ExternalCallImages != nil {
		t.Fatal("ExternalCallImages set without an MCP registry")
	}
}

func TestNewRunExecutorWiring(t *testing.T) {
	var stream, log bytes.Buffer
	specs := []model.ToolSpec{{}}
	called := false
	call := func(ctx context.Context, name, args string) (string, bool, bool) {
		called = true
		return "", false, true
	}
	cmdTools := []*tools.CommandTool{{}}
	extra := []tools.ExtraDir{{}}
	w := toolWiring{
		root:       "/r",
		stream:     &stream,
		cmdTools:   cmdTools,
		commandLog: &log,
		extSpecs:   specs,
		extCall:    call,
		mcp:        &mcptools.Registry{},
		subagents:  &agent.Subagents{},
		workers:    &agent.Workers{},
		extraDirs:  extra,
	}
	e := newRunExecutor(config.AgentConfig{}, w)
	if e.Stream != &stream || e.CommandLog != &log {
		t.Fatal("stream or command log not wired")
	}
	if len(e.CommandTools) != 1 || e.CommandTools[0] != cmdTools[0] {
		t.Fatal("command tools not wired")
	}
	if len(e.ExternalSpecs) != 1 || e.ExternalCall == nil {
		t.Fatal("external tools not wired")
	}
	e.ExternalCall(context.Background(), "x", "{}")
	if !called {
		t.Fatal("ExternalCall is not the wired function")
	}
	if e.ExternalCallImages == nil {
		t.Fatal("ExternalCallImages not set with an MCP registry")
	}
	if !e.Delegate || !e.Workers {
		t.Fatalf("delegation not derived from subagents/workers: %+v", e)
	}
	if len(e.ExtraDirs) != 1 {
		t.Fatal("extra dirs not wired")
	}
}

// TestExecutorBuiltInOnePlace guards against fresh runs, --resume and
// chat/ACP sessions drifting apart again: only newRunExecutor may build a
// tools.Executor in this package.
func TestExecutorBuiltInOnePlace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "run_tools.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("tools.Executor{")) {
			t.Errorf("%s builds a tools.Executor directly; use newRunExecutor", f)
		}
	}
}
