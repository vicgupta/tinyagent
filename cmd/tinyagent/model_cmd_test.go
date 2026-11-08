package main

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestModelStatusText(t *testing.T) {
	s := &session{
		modelName: "openai/gpt-4o",
		fallbacks: []string{"openai/gpt-4o-mini"},
		cfg: config.AgentConfig{
			Models: config.Models{
				Auxiliary: config.RoleModels{
					Model: "openai/gpt-5-mini",
				},
			},
			Subagents: config.Subagents{Enabled: true},
		},
	}
	got := modelStatusText(s, styler{})
	for _, want := range []string{
		"model", "openai/gpt-4o",
		"fallback", "openai/gpt-4o-mini",
		"compaction", "openai/gpt-5-mini",
		"delegate", "openai/gpt-4o",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status lacks %q:\n%s", want, got)
		}
	}
	s.fallbacks = nil
	got = modelStatusText(s, styler{})
	if !strings.Contains(got, "fallback") || !strings.Contains(got, "none") {
		t.Errorf("empty fallbacks:\n%s", got)
	}
}
