package routing

import (
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestDelegatedResolutionOrder(t *testing.T) {
	const main = "openai/gpt-4o"
	cases := []struct {
		name               string
		cfg                config.AgentConfig
		task, want, source string
	}{
		{"nothing configured", config.AgentConfig{}, TaskDelegate, main, SourceMain},
		{"worker nothing", config.AgentConfig{}, TaskWorker, main, SourceMain},
		{"role default", config.AgentConfig{Models: config.Models{Delegated: config.RoleModels{Model: "ollama/a"}}}, TaskWorker, "ollama/a", "models.delegated.model"},
		{"alias beats role default", config.AgentConfig{Subagents: config.Subagents{Model: "ollama/b"}, Models: config.Models{Delegated: config.RoleModels{Model: "ollama/a"}}}, TaskDelegate, "ollama/b", "subagents.model"},
		{"worker alias", config.AgentConfig{Subagents: config.Subagents{Workers: &config.Workers{Model: "ollama/w"}}}, TaskWorker, "ollama/w", "subagents.workers.model"},
		{"delegate alias does not apply to workers", config.AgentConfig{Subagents: config.Subagents{Model: "ollama/b"}}, TaskWorker, main, SourceMain},
		{"task override", config.AgentConfig{Models: config.Models{Delegated: config.RoleModels{Model: "ollama/a", Tasks: map[string]string{"delegate": "ollama/t"}}}}, TaskDelegate, "ollama/t", "models.delegated.tasks.delegate"},
	}
	for _, c := range cases {
		r := Delegated(c.cfg, main, c.task)
		if r.Model != c.want || r.Source != c.source || r.Role != RoleDelegated || r.Task != c.task {
			t.Errorf("%s: got %+v, want %s from %s", c.name, r, c.want, c.source)
		}
	}
}
