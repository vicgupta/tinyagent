package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestListPlugins(t *testing.T) { // AC2, AC11, AC18
	hits := checkEnv(t)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	min := t.TempDir()
	os.WriteFile(filepath.Join(min, "plugin.json"), []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`), 0o644)
	code, stdout, stderr := runCLI(t, "--list-plugins", "-c", pluginConfig(t, "plugins:\n  - path: "+min+"\n"))
	if code != 0 || stdout != min+": demo ok\n" {
		t.Fatalf("minimal: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	dir := mcpPlugin(t, fmt.Sprintf(fixtureServer, os.Args[0])+`,"other":{"type":"stdio","command":"node"},"remote":{"type":"sse","url":"https://x"},"bad":{"type":"stdio"}`)
	os.MkdirAll(filepath.Join(dir, "skills", "lint"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "lint", "SKILL.md"), []byte("---\nname: lint\ndescription: Lint.\n---\n"), 0o644)
	cfg := pluginConfig(t, "plugins:\n  - path: "+dir+"\n    mcp_servers:\n      - server: validator\n        alias: kit\n        tools: [echo]\n")
	// Plugin skills are read only from inside --cwd.
	code, stdout, stderr = runCLI(t, "--list-plugins", "--cwd", filepath.Dir(dir), "-c", cfg)
	if code != 0 {
		t.Fatalf("kit: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, want := range []string{dir + ": kit 1.2.0 warnings:", "  skill lint\n", "server validator (stdio): authorized as kit (1 tools)", "server other (stdio): not authorized", "server remote (sse): unsupported transport", "server bad (stdio): invalid:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(cfgHome, "tinyagent")); !os.IsNotExist(err) {
		t.Errorf("--list-plugins created files under the config dir: %v", err)
	}

	bad := t.TempDir()
	code, stdout, _ = runCLI(t, "--list-plugins", "-c", pluginConfig(t, "plugins:\n  - path: "+bad+"\n  - path: "+min+"\n"))
	if code != 1 || !strings.Contains(stdout, bad+": rejected:") || !strings.Contains(stdout, "demo ok") {
		t.Errorf("rejected: exit %d, stdout %q", code, stdout)
	}
	missing := pluginConfig(t, "plugins:\n  - path: "+dir+"\n    mcp_servers:\n      - server: nope\n        alias: kit\n        tools: [echo]\n")
	if code, stdout, _ = runCLI(t, "--list-plugins", "-c", missing); code != 1 || !strings.Contains(stdout, `server "nope" is not in mcp.json`) {
		t.Errorf("missing server: exit %d, stdout %q", code, stdout)
	}
	if code, _, stderr = runCLI(t, "--list-plugins", "-c", cfg, "-t", "x"); code != 2 || !strings.Contains(stderr, "can't be combined with -t") {
		t.Errorf("with -t: exit %d, stderr %q", code, stderr)
	}
	if code, _, _ = runCLI(t, "--list-plugins", "-c", pluginConfig(t, "plugins:\n  - path: relative\n")); code != 2 {
		t.Errorf("relative path: exit %d", code)
	}
	if code, stdout, _ = runCLI(t, "--list-plugins", "-c", pluginConfig(t, "model: openai/gpt-4o\n")); code != 0 || stdout != "no plugins configured\n" {
		t.Errorf("none: exit %d, stdout %q", code, stdout)
	}
	if hits.Load() != 0 {
		t.Errorf("made %d requests", hits.Load())
	}
}

func TestCheckPlugins(t *testing.T) { // AC19
	checkEnv(t)
	t.Setenv("OPENAI_API_KEY", "fake")
	dir := mcpPlugin(t, fmt.Sprintf(fixtureServer, os.Args[0]))
	auth := "    mcp_servers:\n      - server: %s\n        alias: kit\n        tools: [echo]\n"
	cfg := pluginConfig(t, "plugins:\n  - path: "+dir+"\n"+fmt.Sprintf(auth, "validator")+"  - path: "+t.TempDir()+"\n")
	code, stdout, stderr := runCLI(t, "--check", "-m", "openai/gpt-4o", "-c", cfg)
	if code != 0 || !strings.Contains(stdout, "plugins:   2 configured, 1 rejected, 1 authorized MCP servers") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	cfg = pluginConfig(t, "plugins:\n  - path: "+dir+"\n"+fmt.Sprintf(auth, "nope"))
	if code, _, stderr := runCLI(t, "--check", "-m", "openai/gpt-4o", "-c", cfg); code != 2 || !strings.Contains(stderr, `"nope"`) {
		t.Errorf("missing server: exit %d, stderr %q", code, stderr)
	}
}

func TestPluginRecords(t *testing.T) { // AC1, AC16
	if runtime.GOOS == "windows" {
		t.Skip("shell script server")
	}
	pluginToolServer(t, "kit__echo")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := mcpPlugin(t, fmt.Sprintf(fixtureServer, os.Args[0]))
	os.MkdirAll(filepath.Join(dir, "skills", "lint"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "lint", "SKILL.md"), []byte("---\nname: lint\ndescription: Lint.\n---\n"), 0o644)
	cfg := pluginConfig(t, "plugins:\n  - path: "+dir+"\n    mcp_servers:\n      - server: validator\n        alias: kit\n        tools: [echo]\n")
	out := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runSkills(t, filepath.Dir(dir), "-c", cfg, "--skills", "--json", "-o", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	sum := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	want := trajectory.PluginInfo{Name: "kit", Version: "1.2.0", Path: dir, ManifestSHA256: sum("plugin.json"), MCPSHA256: sum("mcp.json"),
		Skills: []string{"lint"}, MCPServers: []trajectory.PluginServerInfo{{Server: "validator", Alias: "kit"}}}
	var tr trajectory.Trajectory
	if b, err := os.ReadFile(out); err != nil || json.Unmarshal(b, &tr) != nil {
		t.Fatalf("reading %s: %v", out, err)
	}
	if got, _ := json.Marshal(tr.Plugins); string(got) != mustJSON(t, []trajectory.PluginInfo{want}) {
		t.Errorf("trajectory plugins = %s", got)
	}
	line := strings.SplitN(stdout, "\n", 2)[0]
	if !strings.Contains(line, `"run.started"`) || !strings.Contains(line, `"plugin_json_sha256":"`+want.ManifestSHA256+`"`) || strings.Contains(line, "TINYAGENT_PLUGIN_FIXTURE") {
		t.Errorf("run.started = %s", line)
	}

	// No plugins key: nothing recorded.
	out2 := filepath.Join(t.TempDir(), "run.json")
	code, stdout, _ = runSkills(t, t.TempDir(), "--json", "-o", out2)
	if b, _ := os.ReadFile(out2); code != 0 || strings.Contains(string(b), `"plugins"`) || strings.Contains(stdout, `"plugins"`) {
		t.Errorf("no-op default recorded plugins: exit %d", code)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestResumePluginsWarns(t *testing.T) { // AC17
	src := &trajectory.Trajectory{Plugins: []trajectory.PluginInfo{{Name: "kit", ManifestSHA256: "a", MCPSHA256: "m"}, {Name: "gone", ManifestSHA256: "x"}}}
	cur := []trajectory.PluginInfo{{Name: "kit", ManifestSHA256: "b", MCPSHA256: "m"}}
	var warned []string
	resumePlugins(src, cur, func(s string) { warned = append(warned, s) })
	if len(warned) != 1 || warned[0] != "plugin kit: plugin.json changed since the checkpoint" {
		t.Errorf("warnings = %q", warned)
	}
	if len(src.Plugins) != 1 || src.Plugins[0].ManifestSHA256 != "b" {
		t.Errorf("plugins not updated: %+v", src.Plugins)
	}
}
