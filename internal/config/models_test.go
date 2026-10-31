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
