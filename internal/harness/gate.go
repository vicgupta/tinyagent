package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GateSpec is the only stage gate: it checks a plan-tasks stage's output
// before the next stage (usually generate-code) starts.
const GateSpec = "spec"

// GateResult is the outcome of SpecGate.
type GateResult struct {
	Passed bool `json:"passed"`
	// Tasks is how many task blocks the plan had.
	Tasks   int      `json:"tasks"`
	Reason  string   `json:"reason,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

// GateDecision is one gate decision, recorded in Result.Gates and emitted
// as a stage.gate event.
type GateDecision struct {
	Stage   int      `json:"stage"` // 1-based
	Skill   string   `json:"skill"`
	Gate    string   `json:"gate"`
	Attempt int      `json:"attempt"` // 1 = first plan, 2 = the re-plan
	Passed  bool     `json:"passed"`
	Reason  string   `json:"reason,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

var (
	// A task block starts at a line like "## Task 1: ...", "Task 2 - ..."
	// or "**Task 3:** ...".
	reGateTask = regexp.MustCompile(`(?i)^\s*(?:#{1,6}\s*)?\**\s*task\b\s*[^\s:.)-]*\s*(?:[:.)-]|\*\*|$)`)
	// "Acceptance criteria:" label, optionally bold, a heading or a bullet.
	reGateCriteria = regexp.MustCompile(`(?i)^\s*(?:#{1,6}\s*|[-*+]\s+)?\**\s*acceptance criteria\s*\**\s*:?\s*\**\s*:?\s*(.*?)\s*$`)
	reGateItem     = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+\S`)
	// "Verify: `cmd`" line, optionally bold or a bullet.
	reGateVerify = regexp.MustCompile("(?i)^\\s*(?:[-*+]\\s+)?\\**\\s*verif(?:y|ication)\\s*\\**\\s*:\\s*\\**\\s*(.*?)\\s*$")
	reGateCode   = regexp.MustCompile("`([^`]+)`")
	reGateAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// SpecGate checks that a plan has at least one task and that every task has
// at least one acceptance-criteria bullet and a "Verify:" line with a
// runnable command in backticks. "Runnable" means the command's program
// (its first word after any VAR=value prefixes) is found on PATH, or, if it
// contains a slash, exists relative to cwd. The command is not run: the
// code it verifies doesn't exist yet.
func SpecGate(plan, cwd string) GateResult {
	type task struct {
		title    string
		criteria bool
		verify   []string // raw Verify: values
	}
	var tasks []*task
	var cur *task
	inCriteria := false
	for _, line := range strings.Split(strings.ReplaceAll(plan, "\r\n", "\n"), "\n") {
		if reGateTask.MatchString(line) && !reGateCriteria.MatchString(line) && !reGateVerify.MatchString(line) {
			cur = &task{title: strings.Trim(strings.TrimSpace(line), "#* ")}
			tasks = append(tasks, cur)
			inCriteria = false
			continue
		}
		if cur == nil {
			continue
		}
		if m := reGateVerify.FindStringSubmatch(line); m != nil {
			cur.verify = append(cur.verify, m[1])
			inCriteria = false
			continue
		}
		if m := reGateCriteria.FindStringSubmatch(line); m != nil {
			inCriteria = true
			continue
		}
		if inCriteria {
			if reGateItem.MatchString(line) {
				cur.criteria = true
			} else if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				inCriteria = false
			}
		}
	}
	r := GateResult{Tasks: len(tasks)}
	if len(tasks) == 0 {
		r.Missing = []string{"tasks"}
		r.Reason = `the plan has no tasks; start each task with a "Task N: <title>" line`
		return r
	}
	var problems []string
	for i, t := range tasks {
		name := fmt.Sprintf("task %d (%s)", i+1, oneLine(t.title))
		if !t.criteria {
			r.Missing = append(r.Missing, fmt.Sprintf("task %d: acceptance criteria", i+1))
			problems = append(problems, name+` has no "Acceptance criteria:" list with at least one bullet`)
		}
		if len(t.verify) == 0 {
			r.Missing = append(r.Missing, fmt.Sprintf("task %d: verify command", i+1))
			problems = append(problems, name+" has no \"Verify: `<command>`\" line")
			continue
		}
		if err := runnable(t.verify[len(t.verify)-1], cwd); err != nil {
			r.Missing = append(r.Missing, fmt.Sprintf("task %d: runnable verify command", i+1))
			problems = append(problems, name+": "+err.Error())
		}
	}
	if len(problems) == 0 {
		r.Passed = true
		return r
	}
	r.Reason = strings.Join(problems, "; ")
	return r
}

// runnable reports whether a Verify: value names a command that can run.
func runnable(v, cwd string) error {
	m := reGateCode.FindStringSubmatch(v)
	if m == nil {
		return fmt.Errorf("verify line %q has no command in backticks", oneLine(v))
	}
	cmd := strings.TrimSpace(m[1])
	var prog string
	for _, w := range strings.Fields(cmd) {
		if !reGateAssign.MatchString(w) {
			prog = w
			break
		}
	}
	if prog == "" {
		return fmt.Errorf("verify command %q is empty", cmd)
	}
	if strings.ContainsAny(prog, `/\`) {
		p := prog
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("verify command %q: %s not found in %s", cmd, prog, cwd)
		}
		return nil
	}
	if _, err := exec.LookPath(prog); err != nil {
		return fmt.Errorf("verify command %q: %s not found on PATH", cmd, prog)
	}
	return nil
}
