package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestModelsConfig(t *testing.T) {
	if d := Default().Models; d.Delegated.Model != "" || len(d.Delegated.Tasks) != 0 {
		t.Errorf("default models = %+v", d)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "models:\n  delegated:\n    model: ollama/llama3.1\n    tasks:\n      worker: anthropic/claude-sonnet-4-5\nsubagents:\n  model: openai/gpt-5-mini\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.Models.Delegated; d.Model != "ollama/llama3.1" || d.Tasks["worker"] != "anthropic/claude-sonnet-4-5" {
		t.Errorf("models.delegated = %+v", d)
	}
	// The same model under the alias and the new key is fine.
	write(t, p, "models:\n  delegated:\n    tasks:\n      delegate: openai/gpt-5-mini\nsubagents:\n  model: openai/gpt-5-mini\n")
	if _, err := Load(p); err != nil {
		t.Errorf("same model under both keys: %v", err)
	}
	for _, c := range []struct{ body, want string }{
		{"models:\n  delegated:\n    tasks:\n      planner: a/b\n", `unknown task "planner"`},
		{"models:\n  delegated:\n    tasks:\n      worker: \"\"\n", "models.delegated.tasks.worker must be"},
		{"models:\n  delegated:\n    model: \"a b\"\n", "models.delegated.model"},
		{"models:\n  main: a/b\n", "main"},
		{"models:\n  delegated:\n    tasks:\n      delegate: a/b\nsubagents:\n  model: c/d\n", "subagents.model \"c/d\" conflicts with models.delegated.tasks.delegate"},
		{"models:\n  delegated:\n    tasks:\n      worker: a/b\nsubagents:\n  workers:\n    model: c/d\n", "subagents.workers.model \"c/d\" conflicts with models.delegated.tasks.worker"},
	} {
		write(t, p, c.body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want one containing %s", c.body, err, c.want)
		}
	}
}

func TestAuxiliaryModelsConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "models:\n  auxiliary:\n    model: openai/gpt-5-mini\n    tasks:\n      review: anthropic/claude-haiku-4-5\n")
	cfg, err := Load(p)
	if err != nil || cfg.Models.Auxiliary.Model != "openai/gpt-5-mini" || cfg.Models.Auxiliary.Tasks["review"] != "anthropic/claude-haiku-4-5" {
		t.Fatalf("auxiliary = %+v, %v", cfg.Models.Auxiliary, err)
	}
	write(t, p, "models:\n  auxiliary:\n    tasks:\n      worker: a/b\n")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), `models.auxiliary.tasks: unknown task "worker"`) {
		t.Errorf("err = %v", err)
	}
}

func TestRoleFallbackModelsConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "models:\n  delegated:\n    fallback_models: [a/b, c/d]\n")
	if cfg, err := Load(p); err != nil || len(cfg.Models.Delegated.FallbackModels) != 2 {
		t.Fatalf("%+v %v", cfg.Models.Delegated, err)
	}
	for _, c := range []struct{ body, want string }{
		{"models:\n  auxiliary:\n    fallback_models: [a/b, a/b]\n", "listed more than once"},
		{"models:\n  delegated:\n    fallback_models: [a, b, c, d]\n", "at most 3"},
		{"models:\n  delegated:\n    fallback_models: [\"\"]\n", "empty entry"},
	} {
		write(t, p, c.body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v", c.body, err)
		}
	}
}

func TestDelegatedRoutesConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "models:\n  delegated:\n    routes:\n      - match: [schema]\n        model: a/b\n      - type: docs\n        model: c/d\n      - type: docs\n        match: [x]\n        model: e/f\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if r := cfg.Models.Delegated.Routes; len(r) != 3 || r[0].Match[0] != "schema" || r[1].Type != "docs" {
		t.Errorf("routes = %+v", r)
	}
	if got := cfg.Models.TaskTypes(); len(got) != 1 || got[0] != "docs" {
		t.Errorf("TaskTypes = %v", got)
	}
	for _, c := range []struct{ body, want string }{
		{"models:\n  delegated:\n    routes:\n      - match: [x]\n", "routes[0]: model is required"},
		{"models:\n  delegated:\n    routes:\n      - model: a/b\n", "set match keywords, a type, or both"},
		{"models:\n  delegated:\n    routes:\n      - match: [\"\"]\n        model: a/b\n", "empty keyword"},
		{"models:\n  delegated:\n    routes:\n      - type: \"two words\"\n        model: a/b\n", "single word"},
		{"models:\n  auxiliary:\n    routes:\n      - type: x\n        model: a/b\n", "only supported under models.delegated"},
	} {
		write(t, p, c.body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.body, err, c.want)
		}
	}
}
