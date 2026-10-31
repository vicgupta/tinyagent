package main

import (
	"context"
	"io"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// toolWiring is what a run's tool executor is built from besides the
// config: the working directory and environment, the external tools (MCP
// servers, WASM plugins, command tools) and the per-run helpers. Fresh runs,
// --resume and chat/ACP turns all build their tools.Executor through
// newRunExecutor, so a new executor field or a change to MCP and
// command-tool wiring reaches every entry point at once.
type toolWiring struct {
	root string
	env  tools.Environment
	// stream receives bash output as it runs; nil turns streaming off.
	stream io.Writer

	cmdTools []*tools.CommandTool
	// commandLog receives command-tool diagnostics (--verbose).
	commandLog io.Writer

	// extSpecs and extCall are the MCP and WASM plugin tools. A resumed run
	// leaves them nil: it refuses checkpoints that used them and does not
	// start configured MCP servers.
	extSpecs []model.ToolSpec
	extCall  func(ctx context.Context, name, args string) (out string, isErr, ok bool)
	// mcp passes MCP image results through when images are on; nil when no
	// MCP servers run (resume).
	mcp *mcptools.Registry

	subagents *agent.Subagents
	workers   *agent.Workers

	webFetch    *tools.WebFetcher
	lsp         *tools.LSP
	outputFiles *tools.OutputFiles
	fileAccess  *tools.FileAccess
	extraDirs   []tools.ExtraDir
	redactor    *eventlog.Redactor
}

// newRunExecutor builds the tools.Executor for one agent run from cfg and w.
func newRunExecutor(cfg config.AgentConfig, w toolWiring) *tools.Executor {
	e := &tools.Executor{
		Root:           w.root,
		ReadOnly:       cfg.ReadOnly,
		Env:            w.env,
		BashTimeout:    time.Duration(cfg.BashTimeoutSec) * time.Second,
		MaxOutputChars: cfg.MaxOutputChars,
		MaxProcesses:   cfg.MaxBackgroundProcesses,
		Stream:         w.stream,
		CommandTools:   w.cmdTools,
		CommandLog:     w.commandLog,
		ExternalSpecs:  w.extSpecs,
		ExternalCall:   w.extCall,
		Plan:           cfg.Plan.Enabled,
		Images:         cfg.Images.Enabled,
		ApplyPatch:     cfg.ApplyPatch,
		Delegate:       w.subagents != nil,
		TaskTypes:      taskTypes(cfg, w.subagents, w.workers),
		Workers:        w.workers != nil,
		AskUser:        cfg.AskUser.Enabled,
		WebFetch:       w.webFetch,
		LSP:            w.lsp,
		OutputFiles:    w.outputFiles,
		FileAccess:     w.fileAccess,
		ExtraDirs:      w.extraDirs,
		MaxImageBytes:  cfg.MaxImageBytes,
		Redactor:       w.redactor,
	}
	if w.mcp != nil {
		e.ExternalCallImages = w.mcp.CallImages(cfg.MaxImageBytes)
	}
	return e
}
