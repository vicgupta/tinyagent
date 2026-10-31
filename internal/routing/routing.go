// Package routing resolves which model serves each role and task: the main
// agent, the delegated children (delegate subagents and workers) and,
// later, auxiliary helper calls. With no models: section and no routing
// flags every task resolves to the main model.
package routing

import (
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
)

// Roles.
const (
	RoleMain      = "main"
	RoleDelegated = "delegated"
	RoleAuxiliary = "auxiliary"
)

// Tasks.
const (
	TaskMain     = "main"
	TaskDelegate = "delegate"
	TaskWorker   = "worker"

	TaskCompaction = "compaction"
	TaskReview     = "review"
)

// SourceMain is the Source of a task that falls back to the main model.
const SourceMain = "main model"

// Route is the resolved model of one task.
type Route struct {
	Role   string `json:"role"`
	Task   string `json:"task"`
	Model  string `json:"model"`
	Source string `json:"source"` // the flag or config key that chose Model, or SourceMain
}

// Main returns the main agent's route.
func Main(spec string) Route {
	return Route{Role: RoleMain, Task: TaskMain, Model: spec, Source: RoleMain}
}

// Delegated resolves a delegated task (TaskDelegate or TaskWorker): the
// task override (models.delegated.tasks.<task>, or its alias
// subagents.model / subagents.workers.model), then the role default
// models.delegated.model, then the main model.
func Delegated(cfg config.AgentConfig, main, task string) Route {
	r := Route{Role: RoleDelegated, Task: task}
	alias, aliasKey := "", ""
	switch task {
	case TaskDelegate:
		alias, aliasKey = cfg.Subagents.Model, "subagents.model"
	case TaskWorker:
		if w := cfg.Subagents.Workers; w != nil {
			alias, aliasKey = w.Model, "subagents.workers.model"
		}
	}
	d := cfg.Models.Delegated
	switch {
	case strings.TrimSpace(d.Tasks[task]) != "":
		r.Model, r.Source = strings.TrimSpace(d.Tasks[task]), "models.delegated.tasks."+task
	case strings.TrimSpace(alias) != "":
		r.Model, r.Source = strings.TrimSpace(alias), aliasKey
	case strings.TrimSpace(d.Model) != "":
		r.Model, r.Source = strings.TrimSpace(d.Model), "models.delegated.model"
	default:
		r.Model, r.Source = main, SourceMain
	}
	return r
}

// Auxiliary resolves an auxiliary task (TaskCompaction or TaskReview):
// --route <task>=, then models.auxiliary.tasks.<task>, then --aux-model,
// then models.auxiliary.model, then the main model.
func Auxiliary(cfg config.AgentConfig, main, task string) Route {
	r := Route{Role: RoleAuxiliary, Task: task}
	f, a := cfg.Models.Flags, cfg.Models.Auxiliary
	switch {
	case strings.TrimSpace(f.Routes[task]) != "":
		r.Model, r.Source = strings.TrimSpace(f.Routes[task]), "--route "+task
	case strings.TrimSpace(a.Tasks[task]) != "":
		r.Model, r.Source = strings.TrimSpace(a.Tasks[task]), "models.auxiliary.tasks."+task
	case strings.TrimSpace(f.AuxModel) != "":
		r.Model, r.Source = strings.TrimSpace(f.AuxModel), "--aux-model"
	case strings.TrimSpace(a.Model) != "":
		r.Model, r.Source = strings.TrimSpace(a.Model), "models.auxiliary.model"
	default:
		r.Model, r.Source = main, SourceMain
	}
	return r
}

// Info is the run.started "models" field: each route keyed by task.
func Info(routes []Route) map[string]any {
	out := make(map[string]any, len(routes))
	for _, r := range routes {
		out[r.Task] = map[string]any{"role": r.Role, "model": r.Model, "source": r.Source}
	}
	return out
}
