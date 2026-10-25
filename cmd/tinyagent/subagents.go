package main

import (
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// newSubagents builds the delegate children's model when subagents are on:
// subagents.model, or the parent's model spec when it is empty. Children
// never stream to the terminal. The parent's reasoning effort and output
// budget carry over only when the child uses the same provider; another
// provider keeps its defaults. It returns nil when subagents are off.
func newSubagents(cfg config.AgentConfig, parentModel, reasoningEffort string) (*agent.Subagents, error) {
	if !cfg.Subagents.Enabled {
		return nil, nil
	}
	spec := strings.TrimSpace(cfg.Subagents.Model)
	if spec == "" {
		spec = parentModel
	}
	wrap := func(err error) error {
		if cfg.Subagents.Model == "" {
			return fmt.Errorf("subagents: %w", err)
		}
		return fmt.Errorf("subagents.model %q: %w", spec, err)
	}
	if err := model.CheckProviderEnv(spec); err != nil {
		return nil, wrap(err)
	}
	// The parent's own key problems surface as they do today; a child on
	// another provider is checked here so it fails before any request.
	if !sameProvider(spec, parentModel) {
		if env := model.MissingKeyEnv(spec); env != "" {
			return nil, wrap(fmt.Errorf("%s is not set", env))
		}
	}
	m, err := model.NewWith(spec, nil, model.Options{OpenAIAPI: model.OpenAITransport(spec, cfg.OpenAIAPI)})
	if err != nil {
		return nil, wrap(err)
	}
	if sameProvider(spec, parentModel) {
		if err := model.SetReasoningEffort(m, spec, reasoningEffort); err != nil {
			return nil, wrap(err)
		}
		if err := model.SetMaxOutputTokens(m, spec, cfg.MaxOutputTokens); err != nil {
			return nil, wrap(err)
		}
	}
	return &agent.Subagents{Model: m, ModelName: spec, MaxSteps: cfg.Subagents.MaxSteps}, nil
}

// sameProvider reports whether two model specs use the same provider; a
// spec without a provider prefix is an openai model.
func sameProvider(a, b string) bool {
	pa, _, ok := strings.Cut(a, "/")
	if !ok {
		pa = "openai"
	}
	pb, _, ok := strings.Cut(b, "/")
	if !ok {
		pb = "openai"
	}
	return pa == pb
}
