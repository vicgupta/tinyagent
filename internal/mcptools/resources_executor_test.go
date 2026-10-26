package mcptools_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// bigResourceText matches the big resource the fixture serves.
var bigResourceText = strings.Repeat("0123456789abcdef", 20<<10)

// TestReadResourceThroughExecutor checks that resource results get the
// executor's redaction and tool output files, as MCP tool results do.
func TestReadResourceThroughExecutor(t *testing.T) {
	t.Setenv("TINYAGENT_MCP_FIXTURE", "1")
	def := config.MCPServer{
		Alias:          "docs",
		Command:        os.Args[0],
		Args:           []string{"-test.run=^TestMCPFixtureProcess$"},
		Env:            []string{"TINYAGENT_MCP_FIXTURE"},
		Resources:      []string{"docs://guides/*"},
		CallTimeoutSec: 1,
	}
	r, err := mcptools.Start(context.Background(), t.TempDir(), []config.MCPServer{def}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	root := t.TempDir()
	files, err := tools.NewOutputFiles(root, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e := &tools.Executor{
		Root:           root,
		MaxOutputChars: 1000,
		ExternalSpecs:  r.Specs,
		ExternalCall:   r.Call,
		OutputFiles:    files,
		Redactor:       eventlog.NewRedactor(nil, "fixture-secret-value-42"),
	}
	call := func(uri string) tools.Result {
		return e.RunResult(context.Background(), model.ToolCall{ID: "c", Type: "function", Function: model.FunctionCall{Name: mcptools.ReadResourceTool, Arguments: `{"server":"docs","uri":"` + uri + `"}`}})
	}
	if res := call("docs://guides/token"); !strings.Contains(res.Text, "token=[REDACTED]") || strings.Contains(res.Text, "fixture-secret-value-42") {
		t.Errorf("redacted read = %q", res.Text)
	}
	res := call("docs://guides/big")
	if res.OutputFile == "" {
		t.Fatalf("big read not saved: %q", res.Text)
	}
	saved, _ := os.ReadFile(filepath.Join(root, res.OutputFile))
	if !strings.HasPrefix(string(saved), "[resource: docs://guides/big, text/markdown]\n"+bigResourceText[:1000]) || !strings.HasSuffix(string(saved), "\n[truncated]") {
		t.Errorf("saved %d bytes", len(saved))
	}
}
