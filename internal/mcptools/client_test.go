package mcptools

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

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
	// Shaped like SDK-generated schemas: a $schema declaration, a nullable
	// field, and string formats.
	sdkSchema := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"properties":           map[string]any{"url": map[string]any{"type": "string", "format": "uri"}, "note": map[string]any{"type": []any{"string", "null"}}},
		"required":             []any{"url"},
		"additionalProperties": false,
	}
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "fetch", InputSchema: sdkSchema}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fetched " + in["url"].(string)}}}, nil, nil
	})
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no such page"}}}, nil, nil
	})
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "late"}}}, nil, nil
	})
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "union", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}}}}}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// startFixture connects to the fixture server above with the given allowlist.
func startFixture(t *testing.T, def config.MCPServer) (*Registry, error) {
	t.Helper()
	t.Setenv("TINYAGENT_MCP_FIXTURE", "1")
	def.Alias = "docs"
	def.Command = os.Args[0]
	def.Args = []string{"-test.run=^TestMCPFixtureProcess$"}
	def.Env = []string{"TINYAGENT_MCP_FIXTURE"}
	var stderr bytes.Buffer
	r, err := Start(context.Background(), t.TempDir(), []config.MCPServer{def}, &stderr)
	if r != nil {
		t.Cleanup(r.Close)
	}
	return r, err
}

func TestStartAndCallPreservesJSONArguments(t *testing.T) {
	r, err := startFixture(t, config.MCPServer{Tools: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Specs) != 1 || r.Specs[0].Function.Name != "docs__echo" {
		t.Fatalf("catalog = %#v", r.Specs)
	}
	got, isErr, ok := r.Call(context.Background(), "docs__echo", `{"payload":{"nested":[true,12.5,{"key":"v"}]},"extra_name":[false]}`)
	if !ok || isErr {
		t.Fatalf("Call = %q, isErr %v, ok %v", got, isErr, ok)
	}
	const want = `{"extra_name":[false],"payload":{"nested":[true,12.5,{"key":"v"}]}}`
	if !strings.Contains(got, want) {
		t.Fatalf("result lost arguments: %s (want containing %s)", got, want)
	}
	if _, _, ok := r.Call(context.Background(), "bash", `{}`); ok {
		t.Error("a built-in tool name was dispatched to MCP")
	}
}

func TestSDKStyleSchemaIsAccepted(t *testing.T) {
	r, err := startFixture(t, config.MCPServer{Tools: []string{"fetch"}})
	if err != nil {
		t.Fatal(err)
	}
	params := r.Specs[0].Function.Parameters
	if _, ok := params["$schema"]; ok {
		t.Errorf("$schema was sent to the provider: %#v", params)
	}
	got, isErr, _ := r.Call(context.Background(), "docs__fetch", `{"url":"https://example.com","note":null}`)
	if isErr || got != "fetched https://example.com" {
		t.Errorf("Call = %q, isErr %v", got, isErr)
	}
}

func TestUnsupportedSchemaFailsStartup(t *testing.T) {
	_, err := startFixture(t, config.MCPServer{Tools: []string{"union"}})
	if err == nil || !strings.Contains(err.Error(), `keyword "anyOf" is unsupported`) {
		t.Fatalf("err = %v, want the anyOf keyword rejected", err)
	}
}

func TestToolErrorIsFlagged(t *testing.T) {
	r, err := startFixture(t, config.MCPServer{Tools: []string{"fail"}})
	if err != nil {
		t.Fatal(err)
	}
	got, isErr, ok := r.Call(context.Background(), "docs__fail", `{}`)
	if !ok || !isErr || got != "tool error: no such page" {
		t.Errorf("Call = %q, isErr %v, ok %v", got, isErr, ok)
	}
}

func TestCallTimeoutKeepsServerUsable(t *testing.T) {
	r, err := startFixture(t, config.MCPServer{Tools: []string{"slow", "fetch"}, CallTimeoutSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, isErr, _ := r.Call(context.Background(), "docs__slow", `{}`)
	if !isErr || !strings.Contains(got, "timed out after 1s") {
		t.Fatalf("slow Call = %q, isErr %v", got, isErr)
	}
	got, isErr, _ = r.Call(context.Background(), "docs__fetch", `{"url":"u"}`)
	if isErr || got != "fetched u" {
		t.Errorf("call after a timeout = %q, isErr %v; want the server still usable", got, isErr)
	}
}
