package harness

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const behaviorSkill = "# Skill: verify-behavior\n> Intent: Run the built program and check its observed output.\n\n**Inputs**\n- task\n\n**Success check:** every behavioral criterion's case passes.\n"

func loadBehaviorWorkflow(t *testing.T, stage string) *Workflow {
	t.Helper()
	dir := writeFiles(t, t.TempDir(), map[string]string{
		"skills/verify-behavior.md": behaviorSkill,
		"wf.yaml":                   "version: 1\nworkflows:\n  build:\n    stages:\n" + stage,
	})
	w, err := Load(filepath.Join(dir, "wf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// behaviorAgent writes files[call] (if non-empty) to cwd/rel on each call.
func behaviorAgent(t *testing.T, cwd, rel string, files []string, prompts *[]string) RunAgent {
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
		n := len(*prompts)
		if n > len(files) {
			t.Fatalf("unexpected call %d", n)
		}
		if _, err := os.Stat(filepath.Join(cwd, rel)); err == nil {
			t.Errorf("call %d: stale behavior file was not removed", n)
		}
		if f := files[n-1]; f != "" {
			p := filepath.Join(cwd, rel)
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(f), 0o600); err != nil {
				return err
			}
		}
		return os.WriteFile(out, []byte(`{"exit_status":"Submitted","submission":"wrote cases"}`), 0o600)
	}
}

func runBehaviorWorkflow(t *testing.T, retries int, files []string, preexisting bool) (*Result, []string, string) {
	t.Helper()
	w := loadBehaviorWorkflow(t, "      - skill: verify-behavior\n        behavior: .tinyagent/behavior.yaml\n        retries: "+string(rune('0'+retries))+"\n")
	r, err := w.Route("", "build")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if preexisting {
		writeFiles(t, cwd, map[string]string{".tinyagent/behavior.yaml": "version: 1\ncases:\n  - run: [false]\n    expect: {exit_code: 0}\n"})
	}
	var prompts []string
	res := Run(context.Background(), Options{Route: r, Task: "the CLI prints hi", Cwd: cwd, Dir: t.TempDir(), RunAgent: behaviorAgent(t, cwd, ".tinyagent/behavior.yaml", files, &prompts)})
	return res, prompts, cwd
}

func TestBehaviorStagePassesOnObservedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	spec := "version: 1\ncases:\n  - name: greets\n    criterion: prints hi\n    run: [sh, -c, \"echo hi there; echo warn >&2\"]\n    expect:\n      stdout_contains: [hi]\n      stdout_matches: [\"^hi\"]\n      stdout_not_contains: [panic]\n      stderr_contains: [warn]\n"
	res, prompts, _ := runBehaviorWorkflow(t, 0, []string{spec}, true)
	if res.Status != StatusPassed {
		t.Fatalf("result = %+v", res)
	}
	c := res.Attempts[0].Check
	if c.Method != "behavior" || !c.Passed || c.Skipped || len(c.Evidence) != 1 {
		t.Fatalf("check = %+v", c)
	}
	ev := c.Evidence[0]
	if ev.Name != "greets" || !ev.Passed || !strings.Contains(ev.Stdout, "hi there") || !strings.Contains(ev.Stderr, "warn") {
		t.Errorf("evidence = %+v", ev)
	}
	if !strings.Contains(prompts[0], ".tinyagent/behavior.yaml") || !strings.Contains(prompts[0], "stdout_contains") {
		t.Errorf("prompt doesn't explain the behavior file:\n%s", prompts[0])
	}
	log, err := os.ReadFile(res.Attempts[0].Log)
	if err != nil || !strings.Contains(string(log), "verify-behavior evidence") || !strings.Contains(string(log), "hi there") {
		t.Errorf("log = %q, %v", log, err)
	}
}

func TestBehaviorStageFailsThenRetries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	bad := "version: 1\ncases:\n  - name: greets\n    run: [sh, -c, \"echo bye; exit 2\"]\n    expect:\n      stdout_contains: [hi]\n"
	good := "version: 1\ncases:\n  - name: greets\n    run: [sh, -c, \"echo hi\"]\n    expect:\n      stdout_contains: [hi]\n"
	res, prompts, _ := runBehaviorWorkflow(t, 1, []string{bad, good}, false)
	if res.Status != StatusPassed || len(res.Attempts) != 2 {
		t.Fatalf("result = %+v", res)
	}
	c := res.Attempts[0].Check
	if c.Passed || len(c.Evidence) != 1 || c.Evidence[0].ExitCode != 2 {
		t.Fatalf("first check = %+v", c)
	}
	for _, want := range []string{"exit code 2, want 0", `stdout lacks "hi"`} {
		if !strings.Contains(c.Reason, want) || !strings.Contains(prompts[1], want) {
			t.Errorf("reason/retry prompt missing %q: %q", want, c.Reason)
		}
	}
}

func TestBehaviorStageFailsWorkflow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	bad := "version: 1\ncases:\n  - run: [sh, -c, \"echo panic: boom\"]\n    expect:\n      stdout_not_contains: [panic]\n"
	res, _, _ := runBehaviorWorkflow(t, 0, []string{bad}, false)
	if res.Status != StatusFailed || res.FailedStage != "verify-behavior" || !strings.Contains(res.Reason, `stdout contains "panic"`) {
		t.Fatalf("result = %+v", res)
	}
}

func TestBehaviorStageSkipsWithoutCriteria(t *testing.T) {
	for name, file := range map[string]string{"no file": "", "no cases": "version: 1\ncases: []\n"} {
		res, _, _ := runBehaviorWorkflow(t, 0, []string{file}, true)
		c := res.Attempts[0].Check
		if res.Status != StatusPassed || !c.Passed || !c.Skipped || !strings.Contains(c.Reason, "no behavioral acceptance criteria") {
			t.Errorf("%s: result %+v check %+v", name, res, c)
		}
		log, _ := os.ReadFile(res.Attempts[0].Log)
		if !strings.Contains(string(log), "skipped:") {
			t.Errorf("%s: log = %q", name, log)
		}
	}
}

func TestBehaviorStageInvalidFileFails(t *testing.T) {
	res, _, _ := runBehaviorWorkflow(t, 0, []string{"version: 1\ncases:\n  - run: [echo]\n"}, false)
	if res.Status != StatusFailed || !strings.Contains(res.Reason, "expect needs at least one") {
		t.Fatalf("result = %+v", res)
	}
}

func TestBehaviorTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sleep")
	}
	r := runBehaviorCase(context.Background(), t.TempDir(), BehaviorCase{Run: []string{"sleep", "5"}, Timeout: 30, Expect: BehaviorExpect{StdoutContains: []string{"x"}}}, 1)
	if r.Passed || !r.TimedOut || !strings.Contains(strings.Join(r.Failures, ";"), "timed out after 1s") {
		t.Errorf("result = %+v", r)
	}
	r = runBehaviorCase(context.Background(), t.TempDir(), BehaviorCase{Run: []string{"no-such-binary-xyz"}, Expect: BehaviorExpect{}}, 5)
	if r.Passed || !strings.Contains(strings.Join(r.Failures, ";"), "could not run") {
		t.Errorf("missing binary = %+v", r)
	}
}

func TestParseBehavior(t *testing.T) {
	for name, src := range map[string]string{
		"bad version":   "version: 2\n",
		"empty run":     "cases:\n  - run: []\n    expect: {exit_code: 0}\n",
		"no expect":     "cases:\n  - run: [echo]\n",
		"bad regex":     "cases:\n  - run: [echo]\n    expect: {stdout_matches: [\"(\"]}\n",
		"unknown field": "cases:\n  - run: [echo]\n    expect: {stdout_has: [x]}\n",
		"neg timeout":   "cases:\n  - run: [echo]\n    timeout: -1\n    expect: {exit_code: 0}\n",
		"too many":      "cases:\n" + strings.Repeat("  - run: [echo]\n    expect: {exit_code: 0}\n", MaxBehaviorCases+1),
	} {
		if _, err := parseBehavior([]byte(src)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	f, err := parseBehavior([]byte("{\"version\": 1, \"cases\": [{\"run\": [\"echo\"], \"expect\": {\"exit_code\": 0}}]}"))
	if err != nil || len(f.Cases) != 1 {
		t.Errorf("JSON form: %+v, %v", f, err)
	}
}

func TestBehaviorStageValidation(t *testing.T) {
	for name, stage := range map[string]string{
		"absolute":   "      - skill: verify-behavior\n        behavior: /tmp/b.yaml\n",
		"escapes":    "      - skill: verify-behavior\n        behavior: ../b.yaml\n",
		"empty":      "      - skill: verify-behavior\n        behavior: \"\"\n",
		"with check": "      - skill: verify-behavior\n        behavior: b.yaml\n        check: [true]\n",
	} {
		dir := writeFiles(t, t.TempDir(), map[string]string{
			"skills/verify-behavior.md": behaviorSkill,
			"wf.yaml":                   "version: 1\nworkflows:\n  build:\n    stages:\n" + stage,
		})
		if _, err := Load(filepath.Join(dir, "wf.yaml")); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A behavior stage needs no skill success check line.
	dir := writeFiles(t, t.TempDir(), map[string]string{
		"skills/vb.md": "# Skill: vb\n> Intent: check behavior\n\n**Inputs**\n- task\n",
		"wf.yaml":      "version: 1\nworkflows:\n  build:\n    stages:\n      - skill: vb\n        behavior: b.yaml\n",
	})
	if _, err := Load(filepath.Join(dir, "wf.yaml")); err != nil {
		t.Errorf("behavior stage without success check: %v", err)
	}
}
