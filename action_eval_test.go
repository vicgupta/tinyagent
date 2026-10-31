package tinyagent_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func actionEvalSuite(t *testing.T, ws, check string) string {
	t.Helper()
	suite := filepath.Join(ws, "suite $(touch PWNED).yaml")
	data := fmt.Sprintf("version: 1\ntrials: 1\nmodels: [openai/gpt-4o]\ntasks:\n  - {id: one, repo: %q, base: HEAD, task: x, checks: [{argv: [git, %s]}]}\n", ws, check)
	if err := os.WriteFile(suite, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return suite
}

func TestActionEval(t *testing.T) {
	bin := buildTinyagent(t)
	for _, tc := range []struct{ name, check, fail, code string }{
		{"pass", "--version", "true", "0"},
		{"threshold fails", "no-such-command", "true", "1"},
		{"threshold tolerated", "no-such-command", "false", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := gitWorkspace(t)
			suite := actionEvalSuite(t, ws, tc.check)
			stub := newStubModel(t, submitReply("done"))
			out := "reports $(touch PWNED)"
			r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, map[string]string{
				"tinyagent-path": bin, "eval": suite, "eval-out": out, "eval-min-pass-rate": "0.8",
				"cost-limit": "5", "time-limit": "60", "args": "--no-stream", "job-summary": "true", "fail-on-error": tc.fail,
			})
			if (r.failed != "") != (tc.code != "0" && tc.fail == "true") || r.outputs["exit-code"] != tc.code {
				t.Fatalf("failed %q outputs %v\n%s", r.failed, r.outputs, r.log)
			}
			if len(stub.requests()) != 1 {
				t.Fatalf("requests %d\n%s", len(stub.requests()), r.log)
			}
			dir := filepath.Join(ws, out)
			if r.outputs["eval-out"] != dir || r.outputs["eval-report"] != filepath.Join(dir, "report.json") {
				t.Fatal(r.outputs)
			}
			var results struct {
				Configs []struct {
					PassRate float64 `json:"pass_rate"`
				} `json:"configs"`
				BelowThreshold []string `json:"below_threshold"`
			}
			if err := json.Unmarshal([]byte(r.outputs["eval-results"]), &results); err != nil || len(results.Configs) != 1 {
				t.Fatalf("eval-results %q: %v", r.outputs["eval-results"], err)
			}
			if (len(results.BelowThreshold) > 0) != (tc.code == "1") {
				t.Fatal(r.outputs["eval-results"])
			}

			for _, f := range []string{"results.jsonl", "report.json", "report.md"} {
				if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.Size() == 0 {
					t.Fatalf("%s: %v", f, err)
				}
			}
			for _, key := range []string{"status", "submission", "structured-output", "steps", "cost-usd", "trajectory", "events"} {
				if r.outputs[key] != "" {
					t.Errorf("%s = %q", key, r.outputs[key])
				}
			}
			if !strings.Contains(r.summary, "# tinyagent eval:") {
				t.Fatal(r.summary)
			}
			if _, err := os.Stat(filepath.Join(ws, "PWNED")); !os.IsNotExist(err) {
				t.Fatal("shell metacharacters executed")
			}
		})
	}
}

func TestActionEvalValidation(t *testing.T) {
	bin := buildTinyagent(t)
	for _, key := range []string{"task", "task-file", "review-base", "review-commit", "attempts", "model", "profile"} {
		t.Run(key, func(t *testing.T) {
			stub := newStubModel(t)
			r := runAction(t, actionEnv{env: stub.env()}, map[string]string{"tinyagent-path": bin, "eval": "suite.yaml", key: "1"})
			if r.failed != "run" || r.outputs["exit-code"] != "2" || !strings.Contains(r.log, key) || len(stub.requests()) != 0 {
				t.Fatalf("%v\n%s", r.outputs, r.log)
			}
		})
	}
	for _, key := range []string{"eval-out", "eval-min-pass-rate"} {
		t.Run(key, func(t *testing.T) {
			r := runAction(t, actionEnv{}, map[string]string{"tinyagent-path": bin, "task": "x", key: "1"})
			if r.failed != "run" || r.outputs["exit-code"] != "2" || !strings.Contains(r.log, "need eval") {
				t.Fatalf("%v\n%s", r.outputs, r.log)
			}
		})
	}
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint("suite invalid ", invalid), func(t *testing.T) {
			ws := gitWorkspace(t)
			suite := filepath.Join(ws, "missing.yaml")
			if invalid {
				os.WriteFile(suite, []byte("invalid: suite\n"), 0600)
			}
			stub := newStubModel(t)
			r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, map[string]string{"tinyagent-path": bin, "eval": suite, "fail-on-error": "false"})
			if r.failed != "run" || r.outputs["exit-code"] != "2" || len(stub.requests()) != 0 {
				t.Fatalf("%v\n%s", r.outputs, r.log)
			}
		})
	}
}

func TestActionEvalDryRun(t *testing.T) {
	bin := buildTinyagent(t)
	ws := gitWorkspace(t)
	suite := actionEvalSuite(t, ws, "--version")
	stub := newStubModel(t)
	r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, map[string]string{"tinyagent-path": bin, "eval": suite, "args": "--dry-run", "job-summary": "true"})
	if r.failed != "" || r.outputs["exit-code"] != "0" || !strings.Contains(r.log, "1 trial(s)") || len(stub.requests()) != 0 || r.outputs["eval-report"] != "" || r.summary != "" {
		t.Fatalf("%v\n%s", r.outputs, r.log)
	}
	if r.outputs["eval-out"] == "" {
		t.Fatal("missing default output directory")
	}
}

func TestActionEvalSummaryLimitAndArgv(t *testing.T) {
	ws := t.TempDir()
	bin := filepath.Join(ws, "tinyagent")
	script := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$@" > "$ARGV_FILE"
while [ "$1" != --eval-out ]; do shift; done
shift
mkdir -p "$1"
printf '{}\n' > "$1/report.json"
head -c 1048577 /dev/zero | tr '\0' x > "$1/report.md"
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(ws, "argv")
	malicious := "5; touch PWNED"
	r := runAction(t, actionEnv{workspace: ws, env: map[string]string{"ARGV_FILE": argv}}, map[string]string{
		"tinyagent-path": bin, "eval": "suite $(touch PWNED).yaml", "cost-limit": malicious, "time-limit": "60", "config": "config with spaces.yaml", "args": "--dry-run", "job-summary": "true",
	})
	if r.failed != "" || !strings.Contains(r.summary, "exceeds the 1 MiB") || len(r.summary) > 1000 {
		t.Fatalf("failed %s summary %.200s\n%s", r.failed, r.summary, r.log)
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--eval\nsuite $(touch PWNED).yaml\n", "--cost-limit\n" + malicious + "\n", "--time-limit\n60\n", "-c\nconfig with spaces.yaml\n", "--dry-run\n"} {
		if !strings.Contains(string(b), flag) {
			t.Errorf("argv %q lacks %q", b, flag)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "PWNED")); !os.IsNotExist(err) {
		t.Fatal("flag executed shell commands")
	}
}
