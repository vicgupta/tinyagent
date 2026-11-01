package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writePlugin(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["plugin.json"] = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + name + `","version":"1.2.0","foo":1}`
	for path, body := range files {
		p := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPluginSkills(t *testing.T) {
	var offered atomic.Bool
	skillsServer(t, &offered)
	kit := writePlugin(t, "kit", map[string]string{
		"skills/lint/SKILL.md":    "---\nname: lint\ndescription: Lint the code.\n---\nlint steps\n",
		"skills/release/SKILL.md": "---\nname: release\ndescription: Plugin release.\n---\n",
	})
	bad := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cfg, []byte("plugins:\n  - path: "+kit+"\n  - path: "+bad+"\n"), 0o644)
	code, stdout, stderr := runSkills(t, skillsRepo(t), "-c", cfg, "--skills", "-v")
	if code != 0 || !offered.Load() {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"plugin " + bad + " rejected", `plugin kit: plugin.json: unknown field "foo" ignored`, "loaded plugin kit 1.2.0", "shadowed by"} {
		if !strings.Contains(stderr+stdout, want) {
			t.Errorf("output lacks %q:\nstderr %s\nstdout %s", want, stderr, stdout)
		}
	}
	if !strings.Contains(stdout, "loaded skill lint (plugin kit)") {
		t.Errorf("stdout %q", stdout)
	}
	dup := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(dup, []byte("plugins:\n  - path: "+kit+"\n  - path: "+writePlugin(t, "kit", map[string]string{})+"\n"), 0o644)
	if code, _, stderr := runSkills(t, skillsRepo(t), "-c", dup); code != 2 || !strings.Contains(stderr, `name "kit"`) {
		t.Errorf("duplicate names: exit %d, stderr %q", code, stderr)
	}
}
