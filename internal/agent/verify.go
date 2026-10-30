package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// verifyNotice opens the user message that sends the model back to verify
// its changes (verify_on_stop). It is fixed text; %s names the configured
// verify commands, or is empty.
const verifyNotice = "The run was not accepted as finished: files changed since the last successful verification. Verify the change now by running the project's tests or build%s, fix anything that fails, then finish again. If you cannot verify it, say why in your final answer."

// changingTools are the built-in file tools whose successful calls count as
// a change for the verify-on-stop guard.
var changingTools = map[string]bool{"write_file": true, "edit_file": true, "apply_patch": true}

func (a *Agent) verifyOn() bool { return a.Config.VerifyOnStop.Enabled }

func (a *Agent) maxVerifyReprompts() int {
	if n := a.Config.VerifyOnStop.MaxReprompts; n > 0 {
		return n
	}
	return config.DefaultVerifyMaxReprompts
}

// noteVerify updates the guard state after one tool call ran (or was
// refused). Successful file edits and bash runs that are not verification
// mark changes pending; a successful verification clears them; a refused
// bash call records that verification is blocked.
func (a *Agent) noteVerify(t *trajectory.Trajectory, call model.ToolCall, res tools.Result) {
	if !a.verifyOn() {
		return
	}
	name := call.Function.Name
	if changingTools[name] {
		if !a.toolRefused && !res.IsError {
			t.VerifyPending = true
		}
		return
	}
	if name != "bash" {
		return
	}
	if a.toolRefused {
		t.VerifyBlocked = true
		return
	}
	t.VerifyBlocked = false
	if !isVerifyCommand(a.Config.VerifyOnStop, bashCommand(call.Function.Arguments)) {
		t.VerifyPending = true
		return
	}
	if bashSucceeded(res) {
		t.VerifyPending = false
	}
}

// verifyStop applies the guard to a final answer. again reports that the
// model was sent back to verify, so the loop must continue; a non-nil done
// is the final trajectory after a failed save. Otherwise the run may finish,
// with t.Unverified set when changes are still unverified.
func (a *Agent) verifyStop(ctx context.Context, t *trajectory.Trajectory, v *view, msg model.Message) (done *trajectory.Trajectory, again bool) {
	if !a.verifyOn() || a.subagentID != "" {
		return nil, false
	}
	t.Unverified = false
	if !t.VerifyPending {
		return nil, false
	}
	switch {
	case t.VerifyBlocked || a.Tools.ReadOnly || !a.Tools.Permits("bash"):
		a.markUnverified(t, "verification commands cannot run (bash is refused or unavailable)")
		return nil, false
	}
	failed, ok := a.runVerifyChecks(ctx, t)
	if !ok {
		return nil, false
	}
	if t.VerifyReprompts >= a.maxVerifyReprompts() {
		a.markUnverified(t, fmt.Sprintf("still unverified after %d verify re-prompts", t.VerifyReprompts))
		return nil, false
	}
	if strings.Contains(msg.Content, SubmitMarker) {
		// Skipped calls still need results so the history stays valid.
		for _, c := range msg.ToolCalls {
			a.recordToolResult(t, v, c, notRunResult, true)
		}
	}
	t.VerifyReprompts++
	a.event("verify.reprompt", map[string]any{"reprompt": t.VerifyReprompts, "max_reprompts": a.maxVerifyReprompts()})
	if a.Verbose && a.Err != nil {
		fmt.Fprintf(a.Err, "verify: unverified changes; asking the model to verify (%d/%d)\n", t.VerifyReprompts, a.maxVerifyReprompts())
	}
	t.Messages = append(t.Messages, model.Message{Role: "user", Content: fmt.Sprintf(verifyNotice, verifyHint(a.Config.VerifyOnStop)) + failed})
	v.add(t, t.Messages[len(t.Messages)-1])
	if err := a.step(t); err != nil {
		return a.saveFailed(t, err), false
	}
	return nil, true
}

// markUnverified records that the run finishes with unverified changes.
func (a *Agent) markUnverified(t *trajectory.Trajectory, reason string) {
	t.Unverified = true
	a.event("verify.unverified", map[string]any{"reason": reason, "reprompts": t.VerifyReprompts})
	if a.Err != nil {
		fmt.Fprintf(a.Err, "warning: finishing with unverified changes: %s\n", reason)
	}
}

// verifyHint names the configured verify commands for the notice.
func verifyHint(c config.VerifyOnStop) string {
	all := append(append([]string(nil), c.Checks...), c.Commands...)
	if len(all) == 0 {
		return ""
	}
	q := make([]string, len(all))
	for i, cmd := range all {
		q[i] = "`" + strings.TrimSpace(cmd) + "`"
	}
	return " (configured: " + strings.Join(q, ", ") + ")"
}

// bashCommand extracts the command argument of a bash call.
func bashCommand(raw string) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal([]byte(raw), &a)
	return strings.TrimSpace(a.Command)
}

// bashSucceeded reports whether a bash result ran to exit code 0.
func bashSucceeded(res tools.Result) bool {
	return !res.IsError && strings.HasPrefix(res.Text, "<returncode>0</returncode>")
}

// isVerifyCommand reports whether cmd counts as verification: one of the
// configured commands (alone or followed by arguments), or a match for one
// of the patterns (DefaultVerifyPatterns when none are configured).
func isVerifyCommand(c config.VerifyOnStop, cmd string) bool {
	if cmd == "" {
		return false
	}
	for _, want := range append(append([]string(nil), c.Checks...), c.Commands...) {
		want = strings.TrimSpace(want)
		if want != "" && (cmd == want || strings.HasPrefix(cmd, want+" ")) {
			return true
		}
	}
	patterns := c.Patterns
	if len(c.Commands) == 0 && len(c.Checks) == 0 && len(patterns) == 0 {
		patterns = config.DefaultVerifyPatterns
	}
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(cmd) {
			return true
		}
	}
	return false
}
