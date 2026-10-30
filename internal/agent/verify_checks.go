package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// checkFailedNotice is appended to the verify notice when a computational
// check failed; %d is its position, %s its command and its output.
const checkFailedNotice = "\n\nComputational check %d of %d failed: `%s`\n%s"

// runVerifyChecks runs the computational checks (verify_on_stop.checks)
// in their configured order, cheapest first, stopping at the first
// failure. Each runs as a bash tool call, so the permission policy, hooks,
// lockdown and sandbox apply. With every check passing, the pending
// changes are cleared and ok is false: the run may finish verified. A
// failure returns its description for the re-prompt and ok true. A refused
// check marks the run unverified and returns ok false. With no checks
// configured it returns ok true so the plain re-prompt applies.
func (a *Agent) runVerifyChecks(ctx context.Context, t *trajectory.Trajectory) (failed string, ok bool) {
	checks := a.Config.VerifyOnStop.Checks
	if len(checks) == 0 {
		return "", true
	}
	for i, cmd := range checks {
		cmd = strings.TrimSpace(cmd)
		args, _ := json.Marshal(map[string]string{"command": cmd})
		call := model.ToolCall{ID: fmt.Sprintf("verify-check-%d-%d", t.VerifyReprompts, i+1), Type: "function"}
		call.Function.Name, call.Function.Arguments = "bash", string(args)
		res := a.runTool(ctx, t, call)
		refused := a.toolRefused
		passed := !refused && bashSucceeded(res)
		a.event("verify.check", map[string]any{"index": i + 1, "command": cmd, "passed": passed})
		if a.Verbose && a.Err != nil {
			fmt.Fprintf(a.Err, "verify: check %d/%d `%s`: %s\n", i+1, len(checks), cmd, map[bool]string{true: "passed", false: "failed"}[passed])
		}
		if refused {
			t.VerifyBlocked = true
			a.markUnverified(t, fmt.Sprintf("computational check `%s` cannot run (bash is refused)", cmd))
			return "", false
		}
		if !passed {
			return fmt.Sprintf(checkFailedNotice, i+1, len(checks), cmd, res.Text), true
		}
	}
	t.VerifyPending = false
	return "", false
}
