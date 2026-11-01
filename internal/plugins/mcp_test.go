package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func mcpRoot(t *testing.T, mcpJSON string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "bin/server"), []byte("#!/bin/sh\n"), 0o755)
	if mcpJSON != "" {
		os.WriteFile(filepath.Join(root, MCPFile), []byte(mcpJSON), 0o644)
	}
	return root
}

const schemaField = `"$schema":"` + MCPSchemaV1 + `"`

func TestLoadMCPEntries(t *testing.T) {
	root := mcpRoot(t, `{`+schemaField+`,"mcpServers":{
		"ok":{"type":"stdio","command":"./bin/server","args":["--data","${PLUGIN_DATA}${PLUGIN_ROOT}"],"env":{"CFG":"${PLUGIN_ROOT}/c.json","PLUGIN_ROOT":"/tmp/x"}},
		"bare":{"type":"stdio","command":"node"},
		"up":{"type":"stdio","command":"../bin/x"},
		"rel":{"type":"stdio","command":"bin/server"},
		"ph":{"type":"stdio","command":"${PLUGIN_ROOT}/bin/server"},
		"cwd":{"type":"stdio","command":"node","cwd":"data"},
		"cwdup":{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}/../x"},
		"extra":{"type":"stdio","command":"node","shell":true},
		"http":{"type":"streamable-http","url":"https://x"},
		"sse":{"type":"sse","url":"https://x"},
		"odd":{"type":"ws"}}}`)
	c := LoadMCP(root)
	if !c.Present || c.Err != nil || len(c.Digest) != 64 {
		t.Fatalf("%+v", c)
	}
	for _, e := range c.Entries {
		want := map[string]bool{"ok": true, "bare": true}[e.Name]
		if (e.Err == nil) != want {
			t.Errorf("%s: err %v", e.Name, e.Err)
		}
		if (e.Name == "http" || e.Name == "sse") && !errors.Is(e.Err, ErrUnsupported) {
			t.Errorf("%s: %v", e.Name, e.Err)
		}
	}
	e, _ := c.Entry("ok")
	def, err := Server(e, config.PluginServer{Alias: "kit", Tools: []string{"echo"}}, root, "/data")
	if err != nil {
		t.Fatal(err)
	}
	if def.Command != filepath.Join(root, "bin/server") || def.Dir != root || def.Args[1] != "/data"+root {
		t.Errorf("%+v", def)
	}
	want := []string{"CFG=" + root + "/c.json", "PLUGIN_ROOT=" + root, "PLUGIN_DATA=/data"}
	if strings.Join(def.SetEnv, ",") != strings.Join(want, ",") {
		t.Errorf("env %q", def.SetEnv)
	}
}

func TestLoadMCPTopLevel(t *testing.T) {
	if c := LoadMCP(mcpRoot(t, "")); c.Present {
		t.Error("absent mcp.json is present")
	}
	for _, body := range []string{
		`{` + schemaField + `,"mcpServers":{},"extra":1}`,
		`{"mcpServers":{}}`,
		`{"$schema":"https://example.com/x.json","mcpServers":{}}`,
		`{` + schemaField + `,"mcpServers":[]}`,
		`[]`,
	} {
		if c := LoadMCP(mcpRoot(t, body)); c.Err == nil {
			t.Errorf("%s: no error", body)
		}
	}
}

func TestServers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // os.UserConfigDir ignores XDG_CONFIG_HOME on macOS
	root := mcpRoot(t, `{`+schemaField+`,"mcpServers":{"v":{"type":"stdio","command":"./bin/server","cwd":"${PLUGIN_DATA}"},"h":{"type":"sse","url":"https://x"}}}`)
	os.WriteFile(filepath.Join(root, ManifestFile), []byte(`{"$schema":"`+SchemaV1+`","name":"kit"}`), 0o644)
	ls, err := Load([]config.Plugin{{Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	if w := ls[0].MCPWarnings(nil); len(w) != 1 || !strings.Contains(w[0], "unsupported") {
		t.Errorf("warnings %q", w)
	}
	auth := func(s string) config.Plugin {
		return config.Plugin{Path: root, MCPServers: []config.PluginServer{{Server: s, Alias: "kit", Tools: []string{"x"}}}}
	}
	data, _ := DataDir("kit")
	if _, err := Servers(ls[0], auth("v"), false); err != nil {
		t.Fatal(err)
	} else if _, err := os.Stat(data); err == nil {
		t.Error("data dir created without create")
	}
	defs, err := Servers(ls[0], auth("v"), true)
	if err != nil || len(defs) != 1 || defs[0].Dir != data {
		t.Fatalf("%+v, %v", defs, err)
	}
	if fi, err := os.Stat(data); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir %v, %v", fi, err)
	}
	for _, s := range []string{"missing", "h"} {
		if _, err := Servers(ls[0], auth(s), false); err == nil || !strings.Contains(err.Error(), "plugin kit") {
			t.Errorf("%s: %v", s, err)
		}
	}
	if _, err := Servers(Loaded{Path: root, Err: errors.New("bad")}, auth("v"), false); err == nil {
		t.Error("rejected plugin with mcp_servers: no error")
	}
}
