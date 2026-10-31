package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
)

// fakeAgent returns a RunAgent that answers each call with the next reply
// (exit status and submission), recording the -t prompt it got.
func fakeAgent(t *testing.T, replies [][2]string, prompts *[]string) RunAgent {
	return func(_ context.Context, args []string, log io.Writer) error {
		var out, task string
		for i := 0; i < len(args)-1; i++ {
			switch args[i] {
			case "-o":
				out = args[i+1]
			case "-t":
				task = args[i+1]
			}
		}
		*prompts = append(*prompts, task)
		if len(*prompts) > len(replies) {
			t.Fatalf("unexpected call %d", len(*prompts))
		}
		r := replies[len(*prompts)-1]
		b, _ := json.Marshal(map[string]any{"exit_status": r[0], "submission": r[1], "steps": 2, "cost": 0.01})
		fmt.Fprintln(log, "fake run")
		return os.WriteFile(out, b, 0o600)
	}
}

func runTest(t *testing.T, w *Workflow, name string, agent RunAgent) (*Result, []map[string]any) {
	t.Helper()
	r, err := w.Route("task text", name)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	res := Run(context.Background(), Options{
		Route: r, Task: "task text", Cwd: t.TempDir(), Dir: t.TempDir(), RunAgent: agent,
		Emit: func(k string, f map[string]any) {
			f["type"] = k
			events = append(events, f)
		},
	})
	return res, events
}

func TestRunChainsStages(t *testing.T) {
	w := loadTestWorkflow(t, twoStage)
	var prompts []string
	res, events := runTest(t, w, "build", fakeAgent(t, [][2]string{
		{"Submitted", "1. task A\nSUCCESS CHECK: PASS"},
		{"Completed", "all green\nSUCCESS CHECK: PASS"},
	}, &prompts))
	if res.Status != StatusPassed || res.ExitCode() != 0 || len(res.Attempts) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(prompts[1], "1. task A") || !strings.Contains(prompts[0], "task text") {
		t.Errorf("stage 2 didn't get stage 1's output:\n%s", prompts[1])
	}
	if strings.Contains(prompts[0], "### previous_output") {
		t.Error("stage 1 got a previous_output input")
	}
	if res.Output != "all green\nSUCCESS CHECK: PASS" {
		t.Errorf("output = %q", res.Output)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e["type"].(string))
	}
	want := "workflow.started stage.started stage.finished stage.started stage.finished workflow.finished"
	if strings.Join(kinds, " ") != want {
		t.Errorf("events = %v", kinds)
	}
	fin := events[2]
	if fin["input"] == "" || fin["output"] == nil || fin["check"].(map[string]any)["passed"] != true {
		t.Errorf("stage.finished = %v", fin)
	}
	if res.Attempts[0].Input == "" || res.Attempts[0].Check.Method != "model" {
		t.Errorf("attempt = %+v", res.Attempts[0])
	}
}

func TestRunRetriesThenStops(t *testing.T) {
	w := loadTestWorkflow(t, twoStage)
	var prompts []string
	res, _ := runTest(t, w, "build", fakeAgent(t, [][2]string{
		{"Submitted", "SUCCESS CHECK: FAIL - task 2 has no verification"},
		{"Submitted", "no verdict here"},
	}, &prompts))
	if res.Status != StatusFailed || res.ExitCode() != 1 || res.FailedStage != "plan-tasks" {
		t.Fatalf("result = %+v", res)
	}
	if len(prompts) != 2 {
		t.Fatalf("calls = %d, want 2 (one retry, later stage not run)", len(prompts))
	}
	if !strings.Contains(prompts[1], "task 2 has no verification") {
		t.Errorf("retry didn't get the failure reason:\n%s", prompts[1])
	}
	if !strings.Contains(res.Reason, "no SUCCESS CHECK") {
		t.Errorf("reason = %q", res.Reason)
	}
}

func TestRunLimitStatusFails(t *testing.T) {
	w := loadTestWorkflow(t, "version: 1\ndefaults:\n  retries: 0\nworkflows:\n  build:\n    stages:\n      - skill: plan-tasks\n      - skill: run-tests\n")
	var prompts []string
	res, _ := runTest(t, w, "build", fakeAgent(t, [][2]string{{"StepLimitExceeded", ""}}, &prompts))
	if res.Status != StatusFailed || !strings.Contains(res.Reason, "StepLimitExceeded") || len(prompts) != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunCommandCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	w := loadTestWorkflow(t, "version: 1\nworkflows:\n  build:\n    stages:\n      - skill: run-tests\n        retries: 1\n        check: [sh, -c, \"test -f ok || { echo missing ok; exit 3; }\"]\n")
	r, _ := w.Route("", "build")
	cwd := t.TempDir()
	calls := 0
	agent := func(_ context.Context, args []string, _ io.Writer) error {
		calls++
		var out string
		for i := range args {
			if args[i] == "-o" {
				out = args[i+1]
			}
		}
		if calls == 2 {
			os.WriteFile(cwd+"/ok", nil, 0o600)
		}
		return os.WriteFile(out, []byte(`{"exit_status":"Submitted","submission":"done"}`), 0o600)
	}
	res := Run(context.Background(), Options{Route: r, Task: "t", Cwd: cwd, Dir: t.TempDir(), RunAgent: agent})
	if res.Status != StatusPassed || calls != 2 {
		t.Fatalf("result = %+v, calls %d", res, calls)
	}
	c := res.Attempts[0].Check
	if c.Method != "command" || c.Passed || c.ExitCode != 3 || !strings.Contains(c.Reason, "missing ok") {
		t.Errorf("first check = %+v", c)
	}
}

func TestRunInterrupted(t *testing.T) {
	w := loadTestWorkflow(t, twoStage)
	r, _ := w.Route("", "build")
	ctx, cancel := context.WithCancel(context.Background())
	agent := func(_ context.Context, args []string, _ io.Writer) error {
		cancel()
		return context.Canceled
	}
	res := Run(ctx, Options{Route: r, Task: "t", Cwd: t.TempDir(), Dir: t.TempDir(), RunAgent: agent})
	if res.Status != StatusInterrupted || res.ExitCode() != 130 {
		t.Fatalf("result = %+v", res)
	}
}

func TestStageArgs(t *testing.T) {
	w := loadTestWorkflow(t, "version: 1\ndefaults:\n  model: anthropic/x\n  step_limit: 7\nworkflows:\n  build:\n    stages:\n      - skill: plan-tasks\n        model: openai/y\n        time_limit: 9\n        inputs: {spec: \"the spec text\"}\n")
	r, _ := w.Route("", "build")
	var got []string
	agent := func(_ context.Context, args []string, _ io.Writer) error {
		got = args
		for i := range args {
			if args[i] == "-o" {
				os.WriteFile(args[i+1], []byte(`{"exit_status":"Submitted","submission":"SUCCESS CHECK: PASS"}`), 0o600)
			}
		}
		return nil
	}
	Run(context.Background(), Options{Route: r, Task: "t", Cwd: "/w", Dir: t.TempDir(), RunAgent: agent, ExtraArgs: []string{"--sandbox"}})
	s := strings.Join(got, " ")
	for _, want := range []string{"--sandbox -m openai/y --step-limit 7 --time-limit 9 --cwd /w -o "} {
		if !strings.Contains(s, want) {
			t.Errorf("args %q missing %q", s, want)
		}
	}
	if !strings.Contains(got[len(got)-1], "the spec text") {
		t.Errorf("declared input not filled:\n%s", got[len(got)-1])
	}
}

func TestModelVerdict(t *testing.T) {
	for out, pass := range map[string]bool{
		"x\nSUCCESS CHECK: PASS":                        true,
		"**SUCCESS CHECK: PASS**":                       true,
		"SUCCESS CHECK: PASS\nSUCCESS CHECK: FAIL - no": false,
		"`SUCCESS CHECK: FAIL - nope`":                  false,
		"done":                                          false,
	} {
		if got := modelVerdict(out); got.Passed != pass {
			t.Errorf("%q: passed = %v, want %v", out, got.Passed, pass)
		}
	}
	if c := modelVerdict("SUCCESS CHECK: FAIL - missing tests"); c.Reason != "missing tests" {
		t.Errorf("reason = %q", c.Reason)
	}
}

const gated = "version: 1\nworkflows:\n  build:\n    stages:\n      - skill: plan-tasks\n        gate: spec\n      - skill: run-tests\n"

const okPlan = "Task 1: build it\nAcceptance criteria:\n- it works\nVerify: `go test ./...`\nSUCCESS CHECK: PASS"

const thinPlan = "Task 1: build it\nSUCCESS CHECK: PASS"

func gateEvents(events []map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range events {
		if e["type"] == "stage.gate" {
			out = append(out, e)
		}
	}
	return out
}

func TestSpecGateAccepts(t *testing.T) {
	w := loadTestWorkflow(t, gated)
	var prompts []string
	res, events := runTest(t, w, "build", fakeAgent(t, [][2]string{
		{"Submitted", okPlan},
		{"Submitted", "SUCCESS CHECK: PASS"},
	}, &prompts))
	if res.Status != StatusPassed || len(prompts) != 2 || len(res.Gates) != 1 || !res.Gates[0].Passed {
		t.Fatalf("result = %+v", res)
	}
	g := gateEvents(events)
	if len(g) != 1 || g[0]["passed"] != true || g[0]["gate"] != "spec" || g[0]["attempt"] != 1 {
		t.Errorf("gate events = %v", g)
	}
}

func TestSpecGateReplansOnce(t *testing.T) {
	w := loadTestWorkflow(t, gated)
	var prompts []string
	res, events := runTest(t, w, "build", fakeAgent(t, [][2]string{
		{"Submitted", thinPlan},
		{"Submitted", okPlan},
		{"Submitted", "SUCCESS CHECK: PASS"},
	}, &prompts))
	if res.Status != StatusPassed || len(prompts) != 3 {
		t.Fatalf("result = %+v, calls %d", res, len(prompts))
	}
	if !strings.Contains(prompts[1], "spec gate rejected") || !strings.Contains(prompts[1], "Skill: plan-tasks") {
		t.Errorf("re-plan prompt lacks the gate reason:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[2], "Skill: run-tests") {
		t.Errorf("stage 2 not run after re-plan")
	}
	if g := gateEvents(events); len(g) != 2 || g[0]["passed"] != false || g[1]["passed"] != true || g[1]["attempt"] != 2 {
		t.Errorf("gate events = %v", g)
	}
	if res.Attempts[1].Attempt != 2 {
		t.Errorf("re-plan attempt number = %d", res.Attempts[1].Attempt)
	}
}

func TestSpecGateSurfacesAfterSecondReject(t *testing.T) {
	w := loadTestWorkflow(t, gated)
	var prompts []string
	res, events := runTest(t, w, "build", fakeAgent(t, [][2]string{
		{"Submitted", thinPlan},
		{"Submitted", thinPlan},
	}, &prompts))
	if res.Status != StatusFailed || res.ExitCode() != 1 || res.FailedStage != "plan-tasks" || !strings.HasPrefix(res.Reason, "spec gate:") {
		t.Fatalf("result = %+v", res)
	}
	for _, p := range prompts {
		if strings.Contains(p, "Skill: run-tests") {
			t.Fatal("next stage ran after the gate rejected twice")
		}
	}
	if len(prompts) != 2 || len(res.Gates) != 2 || len(gateEvents(events)) != 2 {
		t.Errorf("calls %d, gates %+v", len(prompts), res.Gates)
	}
}

func TestSpecGateInterruptDuringReplan(t *testing.T) {
	w := loadTestWorkflow(t, gated)
	r, _ := w.Route("", "build")
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	agent := func(_ context.Context, args []string, _ io.Writer) error {
		calls++
		if calls == 2 {
			cancel()
			return context.Canceled
		}
		for i := range args {
			if args[i] == "-o" {
				b, _ := json.Marshal(map[string]any{"exit_status": "Submitted", "submission": thinPlan})
				os.WriteFile(args[i+1], b, 0o600)
			}
		}
		return nil
	}
	res := Run(ctx, Options{Route: r, Task: "t", Cwd: t.TempDir(), Dir: t.TempDir(), RunAgent: agent})
	if res.Status != StatusInterrupted {
		t.Fatalf("result = %+v", res)
	}
}

func TestNoGateUnchanged(t *testing.T) {
	w := loadTestWorkflow(t, twoStage)
	var prompts []string
	res, events := runTest(t, w, "build", fakeAgent(t, [][2]string{
		{"Submitted", thinPlan},
		{"Submitted", "SUCCESS CHECK: PASS"},
	}, &prompts))
	if res.Status != StatusPassed || len(res.Gates) != 0 || len(gateEvents(events)) != 0 {
		t.Fatalf("result = %+v", res)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), `"gates"`) {
		t.Error("ungated result has a gates field")
	}
}
