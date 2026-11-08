package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/harness"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/selftune"
)

const tuneSkill = "# Skill: impl\n> Intent: Implement the requested change.\n\n**Inputs**\n- spec\n\n**Success check:** the change works as requested.\n"

// tuneEnv is a project with a workflow, a skill and two failed runs.
func tuneEnv(t *testing.T, enabled bool) string {
	t.Helper()
	cwd, _ := memoryEnv(t)
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	old := tuneNow
	tuneNow = func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { tuneNow = old })
	files := map[string]string{
		"wf.yaml":                   "version: 1\nskills_dir: skills\ndefaults:\n  retries: 0\nworkflows:\n  build:\n    stages:\n      - skill: impl\n",
		"skills/impl.md":            tuneSkill,
		".tinyagent/tinyagent.yaml": "self_tuning:\n  enabled: " + map[bool]string{true: "true", false: "false"}[enabled] + "\n",
	}
	for i, task := range []string{"add CSV export", "add TSV export"} {
		res := harness.Result{SchemaVersion: 1, Kind: "workflow", Workflow: "build", Task: task, Status: harness.StatusFailed, FailedStage: "impl", Reason: "missing header row",
			Attempts: []harness.Attempt{{Stage: 1, Skill: "impl", Attempt: 1, Check: harness.Check{Method: "model", Reason: "missing header row"}}}}
		b, _ := json.Marshal(res)
		files[filepath.Join("runs", string(rune('a'+i))+".json")] = string(b)
	}
	for rel, c := range files {
		p := filepath.Join(cwd, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cwd
}

// fakeTuneAgent passes a stage only when its prompt has the tuned skill.
func fakeTuneAgent(t *testing.T, calls *int) {
	old := runWorkflowAgent
	t.Cleanup(func() { runWorkflowAgent = old })
	runWorkflowAgent = func() (harness.RunAgent, error) {
		return func(_ context.Context, args []string, _ io.Writer) error {
			*calls++
			var out, prompt string
			for i := 0; i+1 < len(args); i++ {
				switch args[i] {
				case "-o":
					out = args[i+1]
				case "-t":
					prompt = args[i+1]
				}
			}
			v := "SUCCESS CHECK: FAIL missing header row"
			if strings.Contains(prompt, "(tune ") {
				v = "SUCCESS CHECK: PASS"
			}
			b, _ := json.Marshal(map[string]any{"exit_status": "Submitted", "submission": v, "steps": 1, "cost": 0.01})
			return os.WriteFile(out, b, 0o600)
		}, nil
	}
}

type tuneFakeModel struct{ calls int }

func (m *tuneFakeModel) Query(context.Context, []model.Message, []model.ToolSpec) (model.Message, model.Cost, error) {
	m.calls++
	return model.Message{Role: "assistant", Content: "Also confirm the output starts with the header row."}, model.Cost{USD: 0.001, InputTokens: 200, OutputTokens: 12}, nil
}

func TestTuneOffByDefault(t *testing.T) {
	cwd := tuneEnv(t, false)
	calls := 0
	fakeTuneAgent(t, &calls)
	m := &tuneFakeModel{}
	old := tuneModel
	tuneModel = func(config.AgentConfig, *flag.FlagSet, *options) (model.Model, string, error) {
		return m, "fake/aux", nil
	}
	t.Cleanup(func() { tuneModel = old })
	for _, args := range [][]string{
		{"tune", "propose", "--workflow", "wf.yaml", "--run", "runs/a.json", "--run", "runs/b.json", "--infer"},
		{"tune", "list"},
		{"tune", "validate", "t-000000000000"},
		{"tune", "apply", "t-000000000000", "--digest", "000000000000"},
	} {
		code, out, errOut := runRatchetCLI(t, append(args, "--cwd", cwd)...)
		if code != 2 || !strings.Contains(errOut, "self-tuning is off") {
			t.Fatalf("%v: exit %d\n%s\n%s", args, code, out, errOut)
		}
	}
	if calls != 0 || m.calls != 0 {
		t.Fatalf("agent runs %d, model calls %d", calls, m.calls)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".tinyagent", "observer")); !os.IsNotExist(err) {
		t.Fatal("off wrote state")
	}
}

func TestTuneCLIFlow(t *testing.T) {
	cwd := tuneEnv(t, true)
	calls := 0
	fakeTuneAgent(t, &calls)
	code, out, errOut := runRatchetCLI(t, "tune", "propose", "--workflow", "wf.yaml", "--run", "runs/a.json", "--run", "runs/b.json", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "proposed t-") {
		t.Fatalf("propose: exit %d\n%s\n%s", code, out, errOut)
	}
	id := regexp.MustCompile(`t-[0-9a-f]{12}`).FindString(out)
	code, out, _ = runRatchetCLI(t, "tune", "show", id, "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "+**Success check:**") || !strings.Contains(out, "not validated yet") {
		t.Fatalf("show: %d\n%s", code, out)
	}
	code, _, errOut = runRatchetCLI(t, "tune", "apply", id, "--digest", "000000000000", "--cwd", cwd)
	if code != 1 || !strings.Contains(errOut, "not validated") {
		t.Fatalf("apply unvalidated: %d\n%s", code, errOut)
	}
	code, out, errOut = runRatchetCLI(t, "tune", "validate", id, "--json", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "tune.validated") || calls != 4 {
		t.Fatalf("validate: %d calls %d\n%s\n%s", code, calls, out, errOut)
	}
	c, err := selftune.Load(cwd, id)
	if err != nil || c.Status != selftune.StatusValidated {
		t.Fatalf("load: %v", err)
	}
	code, _, errOut = runRatchetCLI(t, "tune", "apply", id, "--cwd", cwd)
	if code != 1 || !strings.Contains(errOut, "needs explicit approval") {
		t.Fatalf("apply without digest: %d %s", code, errOut)
	}
	code, out, errOut = runRatchetCLI(t, "tune", "apply", id, "--digest", c.Digest, "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "applied "+id) {
		t.Fatalf("apply: %d\n%s\n%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(cwd, "skills", "impl.md")); string(b) != c.After {
		t.Fatal("skill is not the approved change")
	}
	code, out, _ = runRatchetCLI(t, "tune", "list", "--json", "--cwd", cwd)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); code != 0 || err != nil || len(rows) != 1 || rows[0]["status"] != "applied" {
		t.Fatalf("list: %d %v\n%s", code, err, out)
	}
	code, out, _ = runRatchetCLI(t, "--tune-revert", id, "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "reverted") {
		t.Fatalf("revert: %d %s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(cwd, "skills", "impl.md")); string(b) != tuneSkill {
		t.Fatal("revert did not restore the skill")
	}
}

func TestTuneInferUsesAuxModel(t *testing.T) {
	cwd := tuneEnv(t, true)
	m := &tuneFakeModel{}
	old := tuneModel
	tuneModel = func(config.AgentConfig, *flag.FlagSet, *options) (model.Model, string, error) {
		return m, "fake/aux", nil
	}
	t.Cleanup(func() { tuneModel = old })
	code, out, errOut := runRatchetCLI(t, "tune", "propose", "--workflow", filepath.Join(cwd, "wf.yaml"), "--run", filepath.Join(cwd, "runs/a.json"), "--run", filepath.Join(cwd, "runs/b.json"), "--infer", "--json", "--cwd", cwd)
	if code != 0 || m.calls != 1 || !strings.Contains(out, "tune.proposed") || !strings.Contains(out, `"status":"used"`) {
		t.Fatalf("propose --infer: %d calls %d\n%s\n%s", code, m.calls, out, errOut)
	}
}

func TestTuneUsageErrors(t *testing.T) {
	cwd := tuneEnv(t, true)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--tune-list", "--tune-show", "x"}, "one tune command at a time"},
		{[]string{"--tune-digest", "abc"}, "apply only to"},
		{[]string{"--tune-list", "--tune-digest", "abc"}, "can't be combined with --tune-digest"},
		{[]string{"--tune-list", "-t", "do it"}, "can't be combined with -t"},
		{[]string{"--tune-show", "../../x"}, "no candidate"},
		{[]string{"--tune-propose", "--tune-workflow", "wf.yaml", "--tune-run", "runs/a.json"}, "one failure alone"},
		{[]string{"--tune-propose", "--tune-workflow", "wf.yaml", "--tune-run", "../outside.json", "--tune-run", "runs/a.json"}, "inside --cwd"},
	} {
		code, _, errOut := runRatchetCLI(t, append(c.args, "--cwd", cwd)...)
		if code == 0 || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stderr %q, want %q", c.args, code, errOut, c.want)
		}
	}
}

func TestSelfTuningConfigBounds(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]string{
		"self_tuning:\n  enabled: true\n  min_evidence: 1\n":          "min_evidence must be between 2",
		"self_tuning:\n  validation_cost_usd: 500\n":                  "validation_cost_usd must be > 0 and at most 50",
		"self_tuning:\n  max_attempts: 9\n":                           "max_attempts must be between 1 and 3",
		"self_tuning:\n  enabled: true\n  typo: 1\n":                  "typo",
		"models:\n  auxiliary:\n    tasks:\n      tuning: openai/x\n": "",
	} {
		p := filepath.Join(dir, "c.yaml")
		os.WriteFile(p, []byte(body), 0o644)
		_, err := config.Load(p)
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", body, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
	if d := config.Default().SelfTuning; d.Enabled || d.Inference {
		t.Fatal("self-tuning must be off by default")
	}
}
