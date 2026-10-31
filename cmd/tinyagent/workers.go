package main

import (
	"errors"
	"fmt"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/gitwt"
	"github.com/vicgupta/tinyagent/internal/routing"
)

// workersEnabled reports whether config turns the worker tool on.
func workersEnabled(cfg config.AgentConfig) bool {
	return cfg.Subagents.Workers != nil && cfg.Subagents.Workers.Enabled
}

// setWorkers sets subagents.workers.enabled.
func setWorkers(cfg *config.AgentConfig, on bool) {
	if cfg.Subagents.Workers == nil {
		if !on {
			return
		}
		cfg.Subagents.Workers = &config.Workers{}
	} else {
		w := *cfg.Subagents.Workers
		cfg.Subagents.Workers = &w
	}
	cfg.Subagents.Workers.Enabled = on
}

// newWorkers builds the worker children's model when workers are on: the
// delegated route for the worker task (models.delegated.tasks.worker or
// its alias subagents.workers.model, then models.delegated.model, then the
// parent's model spec), with the same provider rules as newSubagents. It
// returns nil when workers are off.
func newWorkers(cfg config.AgentConfig, parentModel, reasoningEffort string) (*agent.Workers, error) {
	if !workersEnabled(cfg) {
		return nil, nil
	}
	w := cfg.Subagents.Workers
	r := routing.Delegated(cfg, parentModel, routing.TaskWorker)
	m, err := routedModel(cfg, r, parentModel, reasoningEffort)
	if err != nil {
		if r.Source == routing.SourceMain {
			return nil, fmt.Errorf("workers: %w", err)
		}
		return nil, err
	}
	routes, err := childRoutes(cfg, parentModel, routing.TaskWorker, reasoningEffort)
	if err != nil {
		return nil, err
	}
	return &agent.Workers{Model: m, ModelName: r.Model, Routes: routes, MaxSteps: w.MaxSteps, Tools: w.Tools, MaxConcurrent: w.MaxConcurrent}, nil
}

// checkWorkers rejects --workers when its preconditions can't hold, before
// any model call: read-only and review runs, and a --cwd outside a Git
// work tree or in a repository without commits.
func checkWorkers(cfg config.AgentConfig, review bool, root string) error {
	if !workersEnabled(cfg) {
		return nil
	}
	if review {
		return errors.New("--workers can't be combined with --review, --review-base or --review-commit: reviews are read-only")
	}
	if cfg.ReadOnly {
		return errors.New("--workers can't be combined with --read-only: workers edit files and run commands")
	}
	top, err := gitwt.Top(root)
	if err != nil {
		return fmt.Errorf("--workers needs a Git repository: %w", err)
	}
	if _, err := gitwt.Head(top); err != nil {
		return fmt.Errorf("--workers needs a commit to start from: %w", err)
	}
	return nil
}
