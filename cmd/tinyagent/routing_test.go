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

func TestRunStartedModelsAuxiliary(t *testing.T) {
	cfg := writeCfg(t, "models:\n  auxiliary:\n    model: openai/gpt-5-mini\n")
	m := runStartedModels(t, "-c", cfg, "--route", "review=openai/gpt-5-nano")
	c, _ := m["compaction"].(map[string]any)
	r, _ := m["review"].(map[string]any)
	if c["model"] != "openai/gpt-5-mini" || c["source"] != "models.auxiliary.model" || c["role"] != "auxiliary" {
		t.Errorf("compaction route = %v", c)
	}
	if r["model"] != "openai/gpt-5-nano" || r["source"] != "--route review" {
		t.Errorf("review route = %v", r)
	}
}

func TestCheckListsAuxRoutes(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	code, out, _ := runCLI(t, "--check", "-c", writeCfg(t, ""), "--cwd", t.TempDir(), "--aux-model", "openai/gpt-5-mini")
	for _, want := range []string{"compaction: openai/gpt-5-mini (auxiliary, from --aux-model)", "review:"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d, --check lacks %q:\n%s", code, want, out)
		}
	}
}

func TestRouteFlagErrors(t *testing.T) {
	for _, c := range []struct{ arg, want string }{
		{"planner=a/b", `unknown task "planner"`},
		{"compaction", "want task=provider/model"},
		{"review=a b", "single provider/model"},
	} {
		code, _, stderr := runCLI(t, "--check", "-c", writeCfg(t, ""), "--route", c.arg)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("--route %s: exit %d, stderr %s", c.arg, code, stderr)
		}
	}
}

func TestDelegatedRoutingFlagsAndFallbacks(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	cfg := writeCfg(t, "models:\n  delegated:\n    fallback_models: [openai/gpt-5-nano]\n")
	code, out, stderr := runCLI(t, "--check", "-c", cfg, "--cwd", t.TempDir(), "--subagents", "--delegate-model", "openai/gpt-5-mini")
	if code != 0 || !strings.Contains(out, "delegate:  openai/gpt-5-mini (delegated, from --delegate-model), fallback openai/gpt-5-nano") {
		t.Errorf("exit %d, --check:\n%s%s", code, out, stderr)
	}
	code, out, _ = runCLI(t, "--check", "-c", cfg, "--cwd", t.TempDir(), "--subagents", "--route", "delegate=openai/gpt-4.1", "--no-fallback")
	if code != 0 || !strings.Contains(out, "delegate:  openai/gpt-4.1 (delegated, from --route delegate)\n") {
		t.Errorf("exit %d, --check with --route and --no-fallback:\n%s", code, out)
	}
	bad := writeCfg(t, "models:\n  delegated:\n    fallback_models: [nosuch/model]\n")
	if code, _, stderr := runCLI(t, "--check", "-c", bad, "--cwd", t.TempDir(), "--subagents", "--delegate-model", "openai/gpt-5-mini"); code == 0 || !strings.Contains(stderr, "models.delegated.fallback_models") {
		t.Errorf("bad fallback: exit %d, stderr %s", code, stderr)
	}
}

func TestRunStartedDelegatedFallbacks(t *testing.T) {
	cfg := writeCfg(t, "models:\n  delegated:\n    model: openai/gpt-5-mini\n    fallback_models: [openai/gpt-5-nano]\n")
	m := runStartedModels(t, "-c", cfg, "--subagents")
	d, _ := m["delegate"].(map[string]any)
	if fb, _ := d["fallback_models"].([]any); len(fb) != 1 || fb[0] != "openai/gpt-5-nano" {
		t.Errorf("delegate route = %v", d)
	}
}
