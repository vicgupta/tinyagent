package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPluginMCPFixtureProcess is the plugin MCP server: its echo tool
// reports the working directory, environment and arguments it got.
func TestPluginMCPFixtureProcess(t *testing.T) {
	if os.Getenv("TINYAGENT_PLUGIN_FIXTURE") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		wd, _ := os.Getwd()
		text := fmt.Sprintf("cwd=%s root=%s data=%s cfg=%s args=%q", wd, os.Getenv("PLUGIN_ROOT"), os.Getenv("PLUGIN_DATA"), os.Getenv("CFG"), os.Args[len(os.Args)-2:])
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})
	mcp.AddTool[map[string]any, any](s, &mcp.Tool{Name: "hidden", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// pluginToolServer replies with one call of tool, then completes; bodies
// records each request.
func pluginToolServer(t *testing.T, tool string) *[]string {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"`+tool+`","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("HOME", t.TempDir())
	return &bodies
}

func mcpPlugin(t *testing.T, servers string) string {
	t.Helper()
	dir := writePlugin(t, "kit", map[string]string{
		"bin/server": "#!/bin/sh\nexec \"$TEST_BIN\" -test.run='^TestPluginMCPFixtureProcess$' -- \"$@\"\n",
		"mcp.json":   `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{` + servers + `}}`,
	})
	os.Chmod(filepath.Join(dir, "bin/server"), 0o755)
	return dir
}

const fixtureServer = `"validator":{"type":"stdio","command":"./bin/server","args":["--data","${PLUGIN_DATA}/x"],"env":{"TINYAGENT_PLUGIN_FIXTURE":"1","TEST_BIN":%q,"CFG":"${PLUGIN_ROOT}/c.json","PLUGIN_ROOT":"/tmp/x"}}`

func pluginConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func TestPluginMCPServer(t *testing.T) { // AC8, AC9, AC11
	if runtime.GOOS == "windows" {
		t.Skip("shell script server")
	}
	bodies := pluginToolServer(t, "kit__echo")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := mcpPlugin(t, fmt.Sprintf(fixtureServer, os.Args[0])+`,"other":{"type":"stdio","command":"node"},"remote":{"type":"sse","url":"https://x"}`)
	root, _ := filepath.EvalSymlinks(dir)
	cfg := pluginConfig(t, "plugins:\n  - path: "+dir+"\n    mcp_servers:\n      - server: validator\n        alias: kit\n        tools: [echo]\n")
	code, _, stderr := runSkills(t, t.TempDir(), "-c", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	data := filepath.Join(userConfigDir(t), "plugin-data", "kit")
	if fi, err := os.Stat(data); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("PLUGIN_DATA %v, %v", fi, err)
	}
	first, last := (*bodies)[0], (*bodies)[len(*bodies)-1]
	if !strings.Contains(first, `"kit__echo"`) || strings.Contains(first, "kit__hidden") {
		t.Errorf("tools offered: %s", first)
	}
	for _, want := range []string{"cwd=" + root, "root=" + root + " ", "data=" + data, "cfg=" + root + "/c.json", data + "/x"} {
		if !strings.Contains(last, want) {
			t.Errorf("tool result lacks %q: %s", want, last)
		}
	}
	if !strings.Contains(stderr, `"remote"`) || !strings.Contains(stderr, "unsupported") {
		t.Errorf("stderr %q", stderr)
	}

	// --read-only starts nothing.
	*bodies = nil
	if code, _, stderr := runSkills(t, t.TempDir(), "-c", cfg, "--read-only"); code != 0 || strings.Contains((*bodies)[0], "kit__echo") || !strings.Contains(stderr, "not started") {
		t.Errorf("read-only: exit %d, %s", code, stderr)
	}
}

func TestPluginMCPConfigErrors(t *testing.T) { // AC3, AC11, AC13, AC14
	pluginToolServer(t, "kit__echo")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := mcpPlugin(t, fmt.Sprintf(fixtureServer, os.Args[0]))
	auth := "    mcp_servers:\n      - server: %s\n        alias: %s\n        tools: [echo]\n"
	bad := t.TempDir()
	many := "mcp_servers:\n"
	for i := 0; i < 8; i++ {
		many += fmt.Sprintf("  - alias: s%d\n    command: x\n    tools: [a]\n", i)
	}
	for name, c := range map[string]struct{ body, want string }{
		"missing server":   {"plugins:\n  - path: " + dir + "\n" + fmt.Sprintf(auth, "nope", "kit"), `server "nope" is not in mcp.json`},
		"rejected plugin":  {"plugins:\n  - path: " + bad + "\n" + fmt.Sprintf(auth, "validator", "kit"), "was rejected"},
		"alias collision":  {"mcp_servers:\n  - alias: kit\n    command: x\n    tools: [a]\nplugins:\n  - path: " + dir + "\n" + fmt.Sprintf(auth, "validator", "kit"), `plugin kit alias "kit" collides with mcp_servers alias "kit"`},
		"too many servers": {many + "plugins:\n  - path: " + dir + "\n" + fmt.Sprintf(auth, "validator", "kit"), "9 servers"},
	} {
		code, _, stderr := runSkills(t, t.TempDir(), "-c", pluginConfig(t, c.body))
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
		}
	}
	// A bad top-level mcp.json only disables MCP; skills still load.
	odd := writePlugin(t, "odd", map[string]string{
		"mcp.json":             `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{},"extra":1}`,
		"skills/lint/SKILL.md": "---\nname: lint\ndescription: Lint.\n---\n",
	})
	code, stdout, stderr := runSkills(t, t.TempDir(), "-c", pluginConfig(t, "plugins:\n  - path: "+odd+"\n"), "--skills", "-v")
	if code != 0 || !strings.Contains(stderr, "MCP is disabled") || !strings.Contains(stdout+stderr, "loaded skill lint") {
		t.Errorf("odd: exit %d, stderr %q", code, stderr)
	}
}
