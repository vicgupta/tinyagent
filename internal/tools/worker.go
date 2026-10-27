package tools

import (
	"fmt"

	"github.com/vicgupta/tinyagent/internal/model"
)

// WorkerToolName runs an implementation subtask in a child agent confined
// to its own Git worktree; ApplyWorkerToolName applies a worker's commit
// to the working tree. The agent loop runs both; the executor never does.
const (
	WorkerToolName      = "worker"
	ApplyWorkerToolName = "apply_worker"
)

// WorkerSpec describes the worker tool.
func WorkerSpec() model.ToolSpec {
	return spec(WorkerToolName, "Hand a well-scoped implementation subtask to a worker subagent with a fresh context. "+
		"It works in its own Git worktree, a snapshot of the current working tree including uncommitted changes, "+
		"and may edit files and run commands there; your working tree is not touched. "+
		"When it finishes, its changes are committed to its own branch and you get back its report, the commit and a diffstat. "+
		"Nothing is applied automatically: review the result, then call apply_worker to apply it. "+
		"Write the task as a complete brief: what to change, where, constraints, and how to verify it.",
		props{
			"task":  "the complete, self-contained brief for the worker",
			"label": "optional short label for logs, events and the commit message",
		}, "task")
}

// ApplyWorkerSpec describes the apply_worker tool.
func ApplyWorkerSpec() model.ToolSpec {
	return spec(ApplyWorkerToolName, "Apply a finished worker's commit to your working tree (not the index). "+
		"It is atomic: it either applies cleanly or changes nothing and lists the conflicting files.",
		props{"worker": "the worker id from the worker result, e.g. w1"}, "worker")
}

// onlyAllows reports whether Only (when set) lets name through.
func (e *Executor) onlyAllows(name string) bool {
	if len(e.Only) == 0 {
		return true
	}
	for _, n := range e.Only {
		if n == name {
			return true
		}
	}
	return false
}

// filterOnly drops the specs Only does not allow.
func (e *Executor) filterOnly(specs []model.ToolSpec) []model.ToolSpec {
	if len(e.Only) == 0 {
		return specs
	}
	out := specs[:0:0]
	for _, s := range specs {
		if e.onlyAllows(s.Function.Name) {
			out = append(out, s)
		}
	}
	return out
}

// notOffered is the result for a call Only refuses.
func notOffered(name string) Result {
	return Result{Text: fmt.Sprintf("error: tool %q is not available to this worker", name), IsError: true}
}
