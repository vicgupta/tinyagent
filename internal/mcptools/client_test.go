package mcptools

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vicgupta/tinyagent/internal/config"
)

func TestMCPFixtureProcess(t *testing.T) {
	if os.Getenv("TINYAGENT_MCP_FIXTURE") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "echo", Description: "return arguments", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"payload": map[string]any{"type": "object"}}}}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, any, error) {
		return nil, in, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestStartAndCallPreservesJSONArguments(t *testing.T) {
	t.Setenv("TINYAGENT_MCP_FIXTURE", "1")
	ctx := context.Background()
	var stderr bytes.Buffer
	r, err := Start(ctx, t.TempDir(), []config.MCPServer{{Alias: "docs", Command: os.Args[0], Args: []string{"-test.run=^TestMCPFixtureProcess$"}, Tools: []string{"echo"}, Env: []string{"TINYAGENT_MCP_FIXTURE"}}}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.Specs) != 1 || r.Specs[0].Function.Name != "docs__echo" {
		t.Fatalf("catalog = %#v", r.Specs)
	}
	got, ok := r.Call(ctx, "docs__echo", `{"payload":{"nested":[true,12.5,{"key":"v"}]},"extra_name":[false]}`)
	if !ok {
		t.Fatal("external tool wasn't dispatched")
	}
	const want = `{"extra_name":[false],"payload":{"nested":[true,12.5,{"key":"v"}]}}`
	if !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("result lost arguments: %s (want containing %s)", got, want)
	}
}
