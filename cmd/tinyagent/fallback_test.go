package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fallbackSetup serves chat replies from a local gateway behind two
// configured providers, local and other.
func fallbackSetup(t *testing.T) (dir, base string, f *fakeGateway) {
	t.Helper()
	dir, _ = openaiAPISetup(t)
	f = &fakeGateway{replies: []string{chatSubmit}}
	srv := f.start(t)
	base = "providers:\n  - id: local\n    api: chat\n    base_url: " + srv.URL + "\n  - id: other\n    api: chat\n    base_url: " + srv.URL + "\n"
	return dir, base, f
}

func TestFallbackModelValidation(t *testing.T) {
	dir, base, f := fallbackSetup(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, c := range []struct {
		name, cfg, want string
		args            []string
	}{
		{"unknown provider", "", `fallback model "nope/x"`, []string{"--fallback-model", "nope/x"}},
		{"missing key", "", `fallback model "anthropic/claude-sonnet-4-5": ANTHROPIC_API_KEY is not set`, []string{"--fallback-model", "anthropic/claude-sonnet-4-5"}},
		{"primary", "", `fallback model "local/m1" is the primary model`, []string{"--fallback-model", "local/m1"}},
		{"duplicate", "", `fallback model "other/m2" is listed more than once`, []string{"--fallback-model", "other/m2", "--fallback-model", "other/m2"}},
		{"too many", "", "at most 3 fallback models", []string{"--fallback-model", "other/a", "--fallback-model", "other/b", "--fallback-model", "other/c", "--fallback-model", "other/d"}},
		{"empty flag", "", "empty model spec", []string{"--fallback-model="}},
		{"conflict", "", "--no-fallback conflicts with --fallback-model", []string{"--no-fallback", "--fallback-model", "other/m2"}},
		{"config duplicate", "fallback_models: [other/m2, other/m2]\n", "listed more than once", nil},
		{"config missing key", "fallback_models: [anthropic/claude-sonnet-4-5]\n", "ANTHROPIC_API_KEY is not set", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"-t", "x", "-m", "local/m1", "--cwd", dir, "-c", providerCfg(t, dir, base+c.cfg)}, c.args...)
			code, _, stderr := runCLI(t, args...)
			if code != 2 || !strings.Contains(stderr, c.want) || f.count() != 0 {
				t.Errorf("exit %d, %d requests; stderr:\n%s", code, f.count(), stderr)
			}
		})
	}
}

func TestFallbackModelsRecorded(t *testing.T) {
	dir, base, f := fallbackSetup(t)
	cfgWith := providerCfg(t, dir, base+"fallback_models: [other/m2]\n")
	for _, c := range []struct {
		name string
		args []string
		want []any
	}{
		{"config", []string{"-c", cfgWith}, []any{"other/m2"}},
		{"flag replaces config", []string{"-c", cfgWith, "--fallback-model", "other/m3", "--fallback-model", "local/m4"}, []any{"other/m3", "local/m4"}},
		{"no-fallback clears config", []string{"-c", cfgWith, "--no-fallback"}, nil},
		{"none", []string{"-c", providerCfg(t, dir, base)}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "run.json")
			args := append([]string{"-t", "x", "-m", "local/m1", "--cwd", dir, "--json", "-o", out}, c.args...)
			code, stdout, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			got, ok := runStarted(t, stdout)["fallback_models"]
			if c.want == nil && ok || c.want != nil && !jsonEqual(got, c.want) {
				t.Errorf("run.started fallback_models = %v, want %v", got, c.want)
			}
			b, _ := os.ReadFile(out)
			var tr map[string]any
			json.Unmarshal(b, &tr)
			if got, ok := tr["fallback_models"]; c.want == nil && ok || c.want != nil && !jsonEqual(got, c.want) {
				t.Errorf("trajectory fallback_models = %v, want %v", got, c.want)
			}
			if _, ok := tr["active_model"]; ok != (c.want != nil) {
				t.Errorf("trajectory active_model present = %v", ok)
			}
			if strings.Contains(stderr, "model fallback") {
				t.Errorf("switched without an outage: %s", stderr)
			}
		})
	}
	// Requests went to the primary only.
	for i, b := range f.bodies {
		if b["model"] != "m1" {
			t.Errorf("request %d model = %v", i, b["model"])
		}
	}
}

func TestFallbackUnpricedWarning(t *testing.T) {
	dir, base, _ := fallbackSetup(t)
	code, _, stderr := runCLI(t, "-t", "x", "-m", "local/m1", "--cwd", dir, "-c", providerCfg(t, dir, base), "--cost-limit", "1", "--fallback-model", "other/m2")
	if code != 0 || !strings.Contains(stderr, "no pricing available for other/m2") {
		t.Errorf("exit %d; stderr:\n%s", code, stderr)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestResumeWithFallbackModel(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	f := &fakeGateway{replies: []string{chatSubmit}}
	srv := f.start(t)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("ANTHROPIC_API_KEY", "")
	// A bad chain fails before any request.
	code, _, stderr := runCLI(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--fallback-model", "anthropic/claude-sonnet-4-5")
	if code != 2 || !strings.Contains(stderr, "resume: fallback model") || f.count() != 0 {
		t.Fatalf("exit %d, %d requests: %s", code, f.count(), stderr)
	}
	code, _, stderr = runCLI(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--fallback-model", "openai/gpt-4o-mini")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	b, _ := os.ReadFile(output)
	var got map[string]any
	json.Unmarshal(b, &got)
	if !jsonEqual(got["fallback_models"], []string{"openai/gpt-4o-mini"}) || got["active_model"] != "openai/gpt-4o" || got["model"] != "openai/gpt-4o" {
		t.Errorf("continued trajectory: fallback_models %v, active_model %v, model %v", got["fallback_models"], got["active_model"], got["model"])
	}
	if f.count() != 1 || f.bodies[0]["model"] != "gpt-4o" {
		t.Errorf("requests %d: %v", f.count(), f.bodies)
	}
}
