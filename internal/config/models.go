package config

import (
	"fmt"
	"sort"
	"strings"
)

// Models configures model routing: which model serves each role and task.
// Every key is optional; an empty models: section (the default) leaves
// every task on the main model, exactly as without it.
type Models struct {
	// Delegated is the role of the child agents: the delegate tool's
	// read-only subagents (task "delegate") and workers (task "worker").
	Delegated RoleModels `yaml:"delegated,omitempty"`
}

// RoleModels is one role's models: a default for the role and optional
// per-task overrides.
type RoleModels struct {
	// Model is the role's default provider/model; "" means the main model.
	Model string `yaml:"model,omitempty"`
	// Tasks maps a task name to its own provider/model.
	Tasks map[string]string `yaml:"tasks,omitempty"`
}

// DelegatedTasks are the task names models.delegated.tasks accepts.
var DelegatedTasks = []string{"delegate", "worker"}

// validateModels checks the models: section and its aliases:
// subagents.model is models.delegated.tasks.delegate and
// subagents.workers.model is models.delegated.tasks.worker, and setting
// both to different models is an error.
func validateModels(cfg *AgentConfig) error {
	d := cfg.Models.Delegated
	if err := checkSpec("models.delegated.model", d.Model); err != nil {
		return err
	}
	keys := make([]string, 0, len(d.Tasks))
	for k := range d.Tasks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !contains(DelegatedTasks, k) {
			return fmt.Errorf("models.delegated.tasks: unknown task %q (want one of %s)", k, strings.Join(DelegatedTasks, ", "))
		}
		if strings.TrimSpace(d.Tasks[k]) == "" {
			return fmt.Errorf("models.delegated.tasks.%s must be a provider/model, got an empty value", k)
		}
		if err := checkSpec("models.delegated.tasks."+k, d.Tasks[k]); err != nil {
			return err
		}
	}
	if a, b := strings.TrimSpace(cfg.Subagents.Model), strings.TrimSpace(d.Tasks["delegate"]); a != "" && b != "" && a != b {
		return fmt.Errorf("subagents.model %q conflicts with models.delegated.tasks.delegate %q; set only one (subagents.model is an alias)", a, b)
	}
	if w := cfg.Subagents.Workers; w != nil {
		if a, b := strings.TrimSpace(w.Model), strings.TrimSpace(d.Tasks["worker"]); a != "" && b != "" && a != b {
			return fmt.Errorf("subagents.workers.model %q conflicts with models.delegated.tasks.worker %q; set only one (subagents.workers.model is an alias)", a, b)
		}
	}
	return nil
}

// checkSpec rejects a model spec containing whitespace.
func checkSpec(key, spec string) error {
	if strings.ContainsAny(strings.TrimSpace(spec), " \t\n") {
		return fmt.Errorf("%s %q must be a single provider/model", key, spec)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
