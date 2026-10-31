package main

import (
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/routing"
)

// newSubagents builds the delegate children's model when subagents are on:
// the delegated route for the delegate task (models.delegated.tasks.delegate
// or its alias subagents.model, then models.delegated.model, then the
// parent's model spec). Children never stream to the terminal. The parent's
// reasoning effort and output budget carry over only when the child uses
// the same provider; another provider keeps its defaults. It returns nil
// when subagents are off.
func newSubagents(cfg config.AgentConfig, parentModel, reasoningEffort string) (*agent.Subagents, error) {
	if !cfg.Subagents.Enabled {
		return nil, nil
	}
	r := routing.Delegated(cfg, parentModel, routing.TaskDelegate)
	m, err := newChildModel(cfg, r.Model, parentModel, reasoningEffort)
	if err != nil {
		if r.Source == routing.SourceMain {
			return nil, fmt.Errorf("subagents: %w", err)
		}
		return nil, fmt.Errorf("%s %q: %w", r.Source, r.Model, err)
	}
	return &agent.Subagents{Model: m, ModelName: r.Model, MaxSteps: cfg.Subagents.MaxSteps}, nil
}

// newChildModel builds a child agent's model for spec. A child on another
// provider than the parent has its key checked here so it fails before any
// request; the parent's own key problems surface as they do today.
func newChildModel(cfg config.AgentConfig, spec, parentModel, reasoningEffort string) (model.Model, error) {
	if err := model.CheckProviderEnv(spec); err != nil {
		return nil, err
	}
	if !sameProvider(spec, parentModel) {
		if env := model.MissingKeyEnv(spec); env != "" {
			return nil, fmt.Errorf("%s is not set", env)
		}
	}
	m, err := model.NewWith(spec, nil, model.Options{OpenAIAPI: model.OpenAITransport(spec, cfg.OpenAIAPI)})
	if err != nil {
		return nil, err
	}
	if sameProvider(spec, parentModel) {
		if err := model.SetReasoningEffort(m, spec, reasoningEffort); err != nil {
			return nil, err
		}
		if err := model.SetMaxOutputTokens(m, spec, cfg.MaxOutputTokens); err != nil {
			return nil, err
		}
	}
	return m, nil
}

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

// modelRoutes lists this run's model routes: the main model, then the
// auxiliary tasks routed away from it, then the delegate and worker
// routes when those tools are on.
func modelRoutes(cfg config.AgentConfig, main string, s *agent.Subagents, w *agent.Workers) []routing.Route {
	routes := append([]routing.Route{routing.Main(main)}, auxRoutes(cfg, main)...)
	if s != nil {
		routes = append(routes, routing.Delegated(cfg, main, routing.TaskDelegate))
	}
	if w != nil {
		routes = append(routes, routing.Delegated(cfg, main, routing.TaskWorker))
	}
	return routes
}
