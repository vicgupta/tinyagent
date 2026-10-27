package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// evalChildEnv makes the test binary act as tinyagent, so --eval can run
// it as the child for each trial (see TestMain).
const evalChildEnv = "TINYAGENT_EVAL_TEST_CHILD"

// newEvalOpenAI is a fake OpenAI server whose reply depends on a mode word
// in the task: MODE_LIMIT always calls bash, MODE_ASK calls ask_user,
// MODE_ENV runs env once; anything else submits.
func newEvalOpenAI(t *testing.T) *int {
	t.Helper()
	var mu sync.Mutex
	n := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		mu.Lock()
		*n++
		mu.Unlock()
		call := func(name string, args map[string]string) map[string]any {
			a, _ := json.Marshal(args)
			return map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "c1", "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}}}
		}
		toolDone := strings.Contains(body, `"role":"tool"`)
		msg := map[string]any{"role": "assistant", "content": "done\n" + agent.SubmitMarker}
		switch {
		case strings.Contains(body, "MODE_LIMIT"):
			msg = call("bash", map[string]string{"command": "true"})
		case strings.Contains(body, "MODE_ASK"):
			msg = call("ask_user", map[string]string{"question": "which one?"})
		case strings.Contains(body, "MODE_ENV") && !toolDone:
			msg = call("bash", map[string]string{"command": "echo plain=$EVAL_PLAIN secret=$EVAL_SECRET"})
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 10}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "sk-eval-test")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_MODEL", "")
	t.Setenv(evalChildEnv, "1")
	return n
}

func evalRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestEvalEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs bash")
	}
	requests := newEvalOpenAI(t)
	t.Setenv("EVAL_PLAIN", "plain-value")
	t.Setenv("EVAL_SECRET", "secret-sentinel-4242")
	repo := evalRepo(t)
	cfg := writeConfig(t, "command_env:\n  exclude: [EVAL_SECRET]\n")
	suite := filepath.Join(t.TempDir(), "suite.yaml")
	os.WriteFile(suite, []byte("version: 1\ntrials: 1\nmodels: [openai/gpt-4o]\ntasks:\n"+
		"  - {id: pass, repo: "+repo+", base: HEAD, task: MODE_PASS, checks: [{argv: [git, --version]}]}\n"+
		"  - {id: fail, repo: "+repo+", base: HEAD, task: MODE_PASS, checks: [{argv: [git, no-such-subcommand]}]}\n"+
		"  - {id: limit, repo: "+repo+", base: HEAD, task: MODE_LIMIT, limits: {step_limit: 1}, checks: [{argv: [git, --version]}]}\n"+
		"  - {id: ask, repo: "+repo+", base: HEAD, task: MODE_ASK, checks: [{argv: [git, --version]}]}\n"+
		"  - {id: env, repo: "+repo+", base: HEAD, task: MODE_ENV, checks: [{argv: [git, --version]}]}\n"), 0o600)

	// Dry run: matrix and bound, no model requests.
	code, stdout, stderr := runCLI(t, "--eval", suite, "--dry-run", "--step-limit", "10")
	if code != 0 || !strings.Contains(stdout, "5 trial(s)") || !strings.Contains(stdout, "worst-case cost: <= $") || *requests != 0 {
		t.Fatalf("dry run: exit %d, %d requests\n%s%s", code, *requests, stdout, stderr)
	}

	out := filepath.Join(t.TempDir(), "out")
	code, _, stderr = runCLI(t, "--eval", suite, "--eval-out", out, "--no-stream", "--ask-user", "-c", cfg, "--eval-min-pass-rate", "0.2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	f, err := os.Open(filepath.Join(out, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := map[string]map[string]any{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r map[string]any
		json.Unmarshal(sc.Bytes(), &r)
		got[r["task_id"].(string)] = r
	}
	want := map[string]string{"pass": "passed", "fail": "failed_check", "limit": "limit", "ask": "NeedsInput", "env": "passed"}
	for id, w := range want {
		if got[id]["outcome"] != w {
			t.Errorf("%s: outcome %v (%v), want %s", id, got[id]["outcome"], got[id]["error"], w)
		}
	}
	if c, _ := got["pass"]["cost_usd"].(float64); c <= 0 {
		t.Errorf("pass cost = %v", got["pass"]["cost_usd"])
	}
	b, _ := os.ReadFile(got["env"]["trajectory"].(string))
	if !strings.Contains(string(b), "plain=plain-value") || strings.Contains(string(b), "secret-sentinel-4242") {
		t.Errorf("command_env not applied to the trial:\n%s", b)
	}
	b, _ = os.ReadFile(got["ask"]["trajectory"].(string))
	if !strings.Contains(string(b), "which one?") {
		t.Error("ask trajectory lacks the pending question")
	}

	// The gate fails when the threshold is above the 3/5 pass rate.
	code, _, _ = runCLI(t, "--eval", suite, "--eval-out", out, "--no-stream", "--ask-user", "-c", cfg, "--eval-min-pass-rate", "0.8")
	if code != 1 {
		t.Errorf("gate exit = %d, want 1", code)
	}
}

func TestEvalFlagErrors(t *testing.T) {
	newEvalOpenAI(t)
	repo := evalRepo(t)
	suite := filepath.Join(t.TempDir(), "suite.yaml")
	os.WriteFile(suite, []byte("version: 1\ntrials: 1\nmodels: [openai/gpt-4o]\ntasks:\n  - {id: a, repo: "+repo+", base: HEAD, task: x, checks: [{argv: [git, --version]}]}\n"), 0o600)
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("version: 1\nmodelz: []\n"), 0o600)
	out := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--eval", suite}, "needs --eval-out"},
		{[]string{"--eval", suite, "--eval-out", out, "-t", "x"}, "can't be combined with -t"},
		{[]string{"--eval", suite, "--eval-out", out, "-m", "openai/gpt-4o"}, "can't be combined with -m"},
		{[]string{"--eval", suite, "--eval-out", out, "--worktree"}, "can't be combined with --worktree"},
		{[]string{"--eval", suite, "--eval-out", out, "--eval-min-pass-rate", "2"}, "between 0 and 1"},
		{[]string{"--eval", bad, "--eval-out", out}, "field modelz not found"},
		{[]string{"--eval-out", out, "-t", "x"}, "apply only to --eval"},
	} {
		code, _, stderr := runCLI(t, c.args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q, want %q", c.args, code, stderr, c.want)
		}
	}
}

func TestForwardArgs(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	args := []string{"--eval", "s.yaml", "--no-stream", "--eval-out=o", "--cost-limit", "5", "-c", "cfg.yaml", "--worktree", "--dry-run", "--step-limit=3", "--eval-keep"}
	got := forwardArgs(fs, args, evalOwn)
	want := []string{"--no-stream", "-c", "cfg.yaml", "--worktree", "--step-limit=3"}
	if !slices.Equal(got, want) {
		t.Errorf("forwardArgs = %v, want %v", got, want)
	}
}
