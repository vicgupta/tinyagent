package main

import (
	"fmt"
	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
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

// childRoutes builds models.delegated.routes for one delegated task;
// nil without routes.
func childRoutes(cfg config.AgentConfig, main, task, reasoningEffort string) ([]agent.ChildRoute, error) {
	base := routing.Delegated(cfg, main, task)
	var out []agent.ChildRoute
	for i, mr := range cfg.Models.Delegated.Routes {
		r := routing.Route{Role: routing.RoleDelegated, Task: task, Model: strings.TrimSpace(mr.Model), Source: fmt.Sprintf("models.delegated.routes[%d]", i)}
		for _, f := range base.Fallbacks {
			if f != r.Model {
				r.Fallbacks = append(r.Fallbacks, f)
			}
		}
		m, err := routedModel(cfg, r, main, reasoningEffort)
		if err != nil {
			return nil, err
		}
		out = append(out, agent.ChildRoute{Match: mr.Match, Type: mr.Type, Model: m, ModelName: r.Model})
	}
	return out, nil
}

// taskTypes is the task_type enum for the delegate and worker tools: the
// route types, when either tool is on.
func taskTypes(cfg config.AgentConfig, s *agent.Subagents, w *agent.Workers) []string {
	if s == nil && w == nil {
		return nil
	}
	return cfg.Models.TaskTypes()
}

// usageByRoleSummary is the summary line's per-role split, e.g.
// "; by role: main 12.0k tokens $0.10, worker (gpt-5-mini) 3.0k tokens
// $0.01"; empty unless a helper or child made requests.
func usageByRoleSummary(t *trajectory.Trajectory) string {
	if !t.RoutedUsage() {
		return ""
	}
	var parts []string
	for _, u := range t.UsageByRole {
		p := fmt.Sprintf("%s %s tokens", u.Task, formatInt(u.InputTokens+u.OutputTokens))
		if u.Task != u.Role {
			p = fmt.Sprintf("%s/%s (%s) %s tokens", u.Role, u.Task, u.Model, formatInt(u.InputTokens+u.OutputTokens))
		}
		if u.CostUSD > 0 {
			p += " " + formatCost(u.CostUSD)
		}
		parts = append(parts, p)
	}
	return "; by role: " + strings.Join(parts, ", ")
}
