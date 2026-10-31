package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runStartedModels runs one fake-provider turn with --json and returns
// run.started's "models" field.
func runStartedModels(t *testing.T, args ...string) map[string]any {
	t.Helper()
	f := &fakeResponses{replies: []string{responsesSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	t.Setenv("OPENAI_API_KEY", "test")
	code, stdout, _ := runCLI(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", t.TempDir(), "--openai-api", "responses", "--no-stream", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.Contains(line, `"run.started"`) {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		m, _ := ev["models"].(map[string]any)
		return m
	}
	t.Fatalf("no run.started in:\n%s", stdout)
	return nil
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunStartedModelsDefault(t *testing.T) {
	m := runStartedModels(t, "-c", writeCfg(t, ""))
	if len(m) != 1 || m["main"].(map[string]any)["model"] != "openai/gpt-5" {
		t.Errorf("models = %v, want only main", m)
	}
}

func TestRunStartedModelsDelegated(t *testing.T) {
	cfg := writeCfg(t, "models:\n  delegated:\n    model: openai/gpt-5-mini\nsubagents:\n  model: openai/gpt-5-nano\n")
	m := runStartedModels(t, "-c", cfg, "--subagents")
	d, _ := m["delegate"].(map[string]any)
	if d["model"] != "openai/gpt-5-nano" || d["source"] != "subagents.model" || d["role"] != "delegated" {
		t.Errorf("delegate route = %v", d)
	}
	if _, ok := m["worker"]; ok {
		t.Errorf("worker route listed with workers off: %v", m)
	}
}

func TestCheckListsDelegatedRoutes(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	cfg := writeCfg(t, "models:\n  delegated:\n    model: openai/gpt-5-mini\n")
	code, out, _ := runCLI(t, "--check", "-c", cfg, "--cwd", t.TempDir(), "--subagents")
	if code != 0 || !strings.Contains(out, "delegate:  openai/gpt-5-mini (delegated, from models.delegated.model)") || strings.Contains(out, "worker:") {
		t.Errorf("exit %d, --check:\n%s", code, out)
	}
	code, out, _ = runCLI(t, "--check", "-c", writeCfg(t, ""), "--cwd", t.TempDir())
	if code != 0 || strings.Contains(out, "delegate:") {
		t.Errorf("exit %d, --check without subagents:\n%s", code, out)
	}
}
