package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/routing"
)

// helperQuery sends one tool-less helper request for an auxiliary task.
// With a routed model for task it goes there first, and if that fails it
// is retried once on the main model; the returned cost covers both
// attempts. Helper calls are not steps, but their cost and tokens count
// toward the run's limits like any other request.
func (a *Agent) helperQuery(ctx context.Context, task string, req []model.Message) (model.Message, model.Cost, error) {
	m := a.Helpers[task]
	if m == nil {
		return a.Model.Query(ctx, req, nil)
	}
	resp, cost, err := m.Query(ctx, req, nil)
	if err == nil || ctx.Err() != nil {
		return resp, cost, err
	}
	if a.Err != nil {
		fmt.Fprintf(a.Err, "warning: %s model failed (%v); retrying on the main model\n", task, err)
	}
	a.routeEvent(routing.RoleAuxiliary, task, a.ModelName, "fallback", a.routeModel(task))
	resp, c2, err := a.Model.Query(ctx, req, nil)
	cost.USD += c2.USD
	cost.InputTokens += c2.InputTokens
	cost.OutputTokens += c2.OutputTokens
	cost.CacheReadTokens += c2.CacheReadTokens
	cost.CacheWriteTokens += c2.CacheWriteTokens
	return resp, cost, err
}

// ChildRoute sends a delegated task whose brief contains any Match
// keyword (case-insensitive), or whose task_type equals Type, to Model.
type ChildRoute struct {
	Match     []string
	Type      string
	Model     model.Model
	ModelName string
}

// pickChild returns the model for a delegated task: the first matching
// route, else def. reason says why ("match \"kw\"", "type t" or
// "default"). Without routes it returns def and "".
func pickChild(routes []ChildRoute, def model.Model, defName, task, taskType string) (model.Model, string, string) {
	if len(routes) == 0 {
		return def, defName, ""
	}
	lower := strings.ToLower(task)
	for _, r := range routes {
		if r.Type != "" && r.Type == taskType {
			return r.Model, r.ModelName, "type " + r.Type
		}
		for _, kw := range r.Match {
			if kw = strings.ToLower(strings.TrimSpace(kw)); kw != "" && strings.Contains(lower, kw) {
				return r.Model, r.ModelName, fmt.Sprintf("match %q", kw)
			}
		}
	}
	return def, defName, "default"
}

// routeEvent emits a model.route event.
func (a *Agent) routeEvent(role, task, modelName, reason, fallbackFrom string) {
	ev := map[string]any{"role": role, "task": task, "model": modelName, "reason": reason}
	if fallbackFrom != "" {
		ev["fallback_from"] = fallbackFrom
	}
	a.event("model.route", ev)
}

// routeModel is the spec of a routed task's model, from Routes.
func (a *Agent) routeModel(task string) string {
	for _, r := range a.Routes {
		if r.Task == task {
			return r.Model
		}
	}
	return ""
}
