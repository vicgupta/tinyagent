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
	// Auxiliary is the role of helper calls made outside the agent loop:
	// context compaction (task "compaction") and the verify_on_stop
	// review (task "review").
	Auxiliary RoleModels `yaml:"auxiliary,omitempty"`
	// Delegated is the role of the child agents: the delegate tool's
	// read-only subagents (task "delegate") and workers (task "worker").
	Delegated RoleModels `yaml:"delegated,omitempty"`
	// Flags holds the command-line routing overrides (--aux-model,
	// --route); they beat the config keys. Never read from a file.
	Flags RouteFlags `yaml:"-"`
}

// RouteFlags are the routing flags of one invocation.
type RouteFlags struct {
	AuxModel      string            // --aux-model
	DelegateModel string            // --delegate-model
	Routes        map[string]string // --route task=spec
	NoFallback    bool              // --no-fallback also clears every role's fallback_models
}

// RoleModels is one role's models: a default for the role and optional
// per-task overrides.
type RoleModels struct {
	// Model is the role's default provider/model; "" means the main model.
	Model string `yaml:"model,omitempty"`
	// Tasks maps a task name to its own provider/model.
	Tasks map[string]string `yaml:"tasks,omitempty"`
	// FallbackModels is the role's own fallback chain, tried in order
	// when its model keeps failing; empty means none. It never inherits
	// the main model's fallback_models.
	FallbackModels []string `yaml:"fallback_models,omitempty"`
}

// AuxiliaryTasks are the task names models.auxiliary.tasks accepts.
var AuxiliaryTasks = []string{"compaction", "review"}

// DelegatedTasks are the task names models.delegated.tasks accepts.
var DelegatedTasks = []string{"delegate", "worker"}

// validateModels checks the models: section and its aliases:
// subagents.model is models.delegated.tasks.delegate and
// subagents.workers.model is models.delegated.tasks.worker, and setting
// both to different models is an error.
func validateModels(cfg *AgentConfig) error {
	if err := validateRole("auxiliary", cfg.Models.Auxiliary, AuxiliaryTasks); err != nil {
		return err
	}
	if err := validateRole("delegated", cfg.Models.Delegated, DelegatedTasks); err != nil {
		return err
	}
	d := cfg.Models.Delegated
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

// validateRole checks one role's model and task keys.
func validateRole(role string, r RoleModels, tasks []string) error {
	if err := checkSpec("models."+role+".model", r.Model); err != nil {
		return err
	}
	if len(r.FallbackModels) > 3 {
		return fmt.Errorf("models.%s.fallback_models: at most 3 fallback models are allowed, got %d", role, len(r.FallbackModels))
	}
	seen := map[string]bool{}
	for _, f := range r.FallbackModels {
		if strings.TrimSpace(f) == "" {
			return fmt.Errorf("models.%s.fallback_models: empty entry", role)
		}
		if err := checkSpec("models."+role+".fallback_models", f); err != nil {
			return err
		}
		if seen[f] {
			return fmt.Errorf("models.%s.fallback_models: %q is listed more than once", role, f)
		}
		seen[f] = true
	}
	keys := make([]string, 0, len(r.Tasks))
	for k := range r.Tasks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !contains(tasks, k) {
			return fmt.Errorf("models.%s.tasks: unknown task %q (want one of %s)", role, k, strings.Join(tasks, ", "))
		}
		if strings.TrimSpace(r.Tasks[k]) == "" {
			return fmt.Errorf("models.%s.tasks.%s must be a provider/model, got an empty value", role, k)
		}
		if err := checkSpec("models."+role+".tasks."+k, r.Tasks[k]); err != nil {
			return err
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
