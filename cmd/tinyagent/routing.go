package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/routing"
)

// routeTasks are the task names --route accepts.
var routeTasks = []string{routing.TaskCompaction, routing.TaskReview, routing.TaskDelegate, routing.TaskWorker}

// routeList is the repeatable --route task=spec flag.
type routeList map[string]string

func (r *routeList) String() string {
	if r == nil || len(*r) == 0 {
		return ""
	}
	keys := make([]string, 0, len(*r))
	for k := range *r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + (*r)[k]
	}
	return strings.Join(parts, ",")
}

func (r *routeList) Set(v string) error {
	task, spec, ok := strings.Cut(v, "=")
	task, spec = strings.TrimSpace(task), strings.TrimSpace(spec)
	if !ok || task == "" || spec == "" {
		return fmt.Errorf("want task=provider/model, got %q", v)
	}
	known := false
	for _, t := range routeTasks {
		known = known || t == task
	}
	if !known {
		return fmt.Errorf("unknown task %q (want one of %s)", task, strings.Join(routeTasks, ", "))
	}
	if strings.ContainsAny(spec, " \t\n") {
		return fmt.Errorf("%q must be a single provider/model", spec)
	}
	if prev, dup := (*r)[task]; dup && prev != spec {
		return fmt.Errorf("task %q routed twice (%s and %s)", task, prev, spec)
	}
	if *r == nil {
		*r = routeList{}
	}
	(*r)[task] = spec
	return nil
}

// newHelpers builds the models of the auxiliary tasks routed away from
// the main model; nil when none are.
func newHelpers(cfg config.AgentConfig, main, reasoningEffort string) (map[string]model.Model, error) {
	var out map[string]model.Model
	for _, task := range config.AuxiliaryTasks {
		r := routing.Auxiliary(cfg, main, task)
		if r.Model == main && len(r.Fallbacks) == 0 {
			continue
		}
		m, err := routedModel(cfg, r, main, reasoningEffort)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]model.Model{}
		}
		out[task] = m
	}
	return out, nil
}

// auxRoutes lists the auxiliary tasks routed away from the main model.
func auxRoutes(cfg config.AgentConfig, main string) []routing.Route {
	var out []routing.Route
	for _, task := range config.AuxiliaryTasks {
		if r := routing.Auxiliary(cfg, main, task); r.Source != routing.SourceMain {
			out = append(out, r)
		}
	}
	return out
}

// routedModel builds the model of a non-main route: r.Model (checked like
// a child model) wrapped in the role's fallback chain, if any. Errors name
// the key or flag that chose the model.
func routedModel(cfg config.AgentConfig, r routing.Route, main, reasoningEffort string) (model.Model, error) {
	wrap := func(err error) error {
		if r.Source == routing.SourceMain {
			return err
		}
		return fmt.Errorf("%s %q: %w", r.Source, r.Model, err)
	}
	m, err := newChildModel(cfg, r.Model, main, reasoningEffort)
	if err != nil {
		return nil, wrap(err)
	}
	if len(r.Fallbacks) == 0 {
		return m, nil
	}
	if err := checkFallbacks(r.Model, r.Fallbacks, cfg.OpenAIAPI); err != nil {
		return nil, fmt.Errorf("models.%s.fallback_models: %w", r.Role, err)
	}
	effort := ""
	if sameProvider(r.Model, main) {
		effort = reasoningEffort
	}
	return withFallbacks(m, r.Model, r.Fallbacks, nil, cfg, effort, os.Stderr)
}
