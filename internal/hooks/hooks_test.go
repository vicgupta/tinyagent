package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
)

func TestMain(m *testing.M) {
	hookstest.Run()
	os.Exit(m.Run())
}

func intp(n int) *int { return &n }

func mustNew(t *testing.T, cfg config.Hooks) *Runner {
	t.Helper()
	r, err := New(cfg, "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewNoHooks(t *testing.T) {
	r, err := New(config.Hooks{}, "x.yaml")
	if r != nil || err != nil {
		t.Fatalf("New = %v, %v", r, err)
	}
	if got := r.Pre(context.Background(), Input{ToolName: "bash"}); got != nil {
		t.Errorf("nil runner Pre = %v", got)
	}
}

func TestNewValidation(t *testing.T) {
	t.Setenv("HOOK_SET", "1")
	abs, _ := filepath.Abs("x")
	cases := []struct {
		name string
		cfg  config.Hooks
		want string
	}{
		{"missing command", config.Hooks{PreTool: []config.Hook{{}}}, "hooks.pre_tool[0]: command is required"},
		{"relative command", config.Hooks{PostTool: []config.Hook{{Command: "fmt-check"}}}, "hooks.post_tool[0]: command \"fmt-check\" must be an absolute path"},
		{"bad regexp", config.Hooks{PreTool: []config.Hook{{Command: abs}, {Command: abs, Match: "("}}}, "hooks.pre_tool[1]: invalid match regexp"},
		{"zero timeout", config.Hooks{PreTool: []config.Hook{{Command: abs, TimeoutSec: intp(0)}}}, "timeout_seconds must be between 1 and 600, got 0"},
		{"large timeout", config.Hooks{PreTool: []config.Hook{{Command: abs, TimeoutSec: intp(601)}}}, "timeout_seconds must be between 1 and 600"},
		{"bad on_error", config.Hooks{PreTool: []config.Hook{{Command: abs, OnError: "ignore"}}}, "on_error must be deny or allow"},
		{"post on_error", config.Hooks{PostTool: []config.Hook{{Command: abs, OnError: "deny"}}}, "on_error applies only to pre_tool"},
		{"missing env", config.Hooks{PreTool: []config.Hook{{Command: abs, Env: []string{"HOOK_SET", "HOOK_SURELY_UNSET_VAR"}}}}, "forwarded environment variable HOOK_SURELY_UNSET_VAR is not set"},
		{"bad env name", config.Hooks{PreTool: []config.Hook{{Command: abs, Env: []string{"A=B"}}}}, "invalid or duplicate"},
	}
	for _, c := range cases {
		_, err := New(c.cfg, "/home/u/config.yaml")
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "/home/u/config.yaml") {
			t.Errorf("%s: err = %v, want %q naming the file", c.name, err, c.want)
		}
	}
}

func TestMatchWholeName(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	r := mustNew(t, config.Hooks{PreTool: []config.Hook{
		hookstest.Hook(t, "edit_file", "log", log, "edit"),
		hookstest.Hook(t, "docs__.*", "log", log, "docs"),
		hookstest.Hook(t, "", "log", log, "all"),
	}})
	for _, name := range []string{"write_file", "docs__search", "xdocs__search"} {
		r.Pre(context.Background(), Input{ToolName: name, Root: t.TempDir()})
	}
	b, _ := os.ReadFile(log)
	if got := string(b); got != "all\ndocs\nall\nall\n" {
		t.Errorf("log = %q", got)
	}
}

func TestPreDecisions(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "no-such-hook")
	sleep := hookstest.Hook(t, "", "sleep")
	sleep.TimeoutSec = intp(1)
	cases := []struct {
		name     string
		hook     config.Hook
		decision string
		denied   bool
		reason   string
	}{
		{"exit 0 empty", hookstest.Hook(t, "", "stdout", ""), Allow, false, ""},
		{"exit 0 allow json", hookstest.Hook(t, "", "stdout", `{"decision":"allow"}`), Allow, false, ""},
		{"json deny", hookstest.Hook(t, "", "stdout", `{"decision":"deny","reason":"protected path"}`), Deny, true, "protected path"},
		{"exit 2", hookstest.Hook(t, "", "exit2", "no network commands\n"), Deny, true, "no network commands"},
		{"exit 1", hookstest.Hook(t, "", "exit", "1"), Error, true, "exited with status 1"},
		{"invalid json", hookstest.Hook(t, "", "stdout", "not json"), Error, true, "invalid JSON"},
		{"unknown decision", hookstest.Hook(t, "", "stdout", `{"decision":"ask"}`), Error, true, "unknown decision"},
		{"missing executable", config.Hook{Command: missing}, Error, true, "failed to run"},
		{"timeout", sleep, Error, true, "timed out after 1s"},
	}
	for _, c := range cases {
		r := mustNew(t, config.Hooks{PreTool: []config.Hook{c.hook}})
		res := r.Pre(context.Background(), Input{ToolName: "bash", Root: root})
		if len(res) != 1 || res[0].Decision != c.decision || res[0].Denied != c.denied || !strings.Contains(res[0].Reason, c.reason) {
			t.Errorf("%s: results = %+v", c.name, res)
			continue
		}
		if c.decision == Error {
			// on_error: allow lets the call proceed after the same failure.
			c.hook.OnError = Allow
			r := mustNew(t, config.Hooks{PreTool: []config.Hook{c.hook}})
			res := r.Pre(context.Background(), Input{ToolName: "bash", Root: root})
			if len(res) != 1 || res[0].Decision != Error || res[0].Denied {
				t.Errorf("%s with on_error allow: results = %+v", c.name, res)
			}
		}
	}
}

func TestPreStopsAtFirstDeny(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	r := mustNew(t, config.Hooks{PreTool: []config.Hook{
		hookstest.Hook(t, "", "log", log, "first"),
		hookstest.Hook(t, "", "exit2", "no"),
		hookstest.Hook(t, "", "log", log, "third"),
	}})
	res := r.Pre(context.Background(), Input{ToolName: "bash", Root: t.TempDir()})
	if len(res) != 2 || !res[1].Denied {
		t.Errorf("results = %+v", res)
	}
	if b, _ := os.ReadFile(log); string(b) != "first\n" {
		t.Errorf("log = %q", b)
	}
}

func TestPostFeedback(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("x", MaxFeedback+100)
	r := mustNew(t, config.Hooks{PostTool: []config.Hook{
		hookstest.Hook(t, "", "stdout", `{"feedback":"main.go is not gofmt-formatted"}`),
		hookstest.Hook(t, "", "exit2", "vet: problem"),
		hookstest.Hook(t, "", "stdout", ""),
		hookstest.Hook(t, "", "exit", "1"),
		hookstest.Hook(t, "", "stdout", "garbage"),
		hookstest.Hook(t, "", "stdout", `{"feedback":"`+long+`"}`),
	}})
	res := r.Post(context.Background(), Input{ToolName: "write_file", Root: root}, "ok", false)
	want := []string{Feedback, Feedback, Allow, Error, Error, Feedback}
	if len(res) != len(want) {
		t.Fatalf("results = %+v", res)
	}
	for i, d := range want {
		if res[i].Decision != d {
			t.Errorf("result %d decision = %s, want %s", i, res[i].Decision, d)
		}
	}
	if !strings.HasPrefix(res[0].Feedback, "\n[post_tool hook ") || !strings.HasSuffix(res[0].Feedback, "]\nmain.go is not gofmt-formatted") {
		t.Errorf("feedback = %q", res[0].Feedback)
	}
	if !strings.HasSuffix(res[1].Feedback, "\nvet: problem") {
		t.Errorf("exit 2 feedback = %q", res[1].Feedback)
	}
	if res[2].Feedback != "" || res[3].Feedback != "" || res[4].Feedback != "" {
		t.Errorf("unexpected feedback: %+v", res[2:5])
	}
	if f := res[5].Feedback; len(f) > MaxFeedback+200 || !strings.HasSuffix(f, "[feedback truncated]") {
		t.Errorf("long feedback not capped: %d bytes", len(f))
	}
}

func TestInputJSON(t *testing.T) {
	dir := t.TempDir()
	pre, post := filepath.Join(dir, "pre"), filepath.Join(dir, "post")
	r := mustNew(t, config.Hooks{
		PreTool:  []config.Hook{hookstest.Hook(t, "", "stdin", pre)},
		PostTool: []config.Hook{hookstest.Hook(t, "", "stdin", post)},
	})
	in := Input{ToolName: "bash", ToolCallID: "c1", Arguments: map[string]any{"command": "ls"}, Root: dir, ReadOnly: false, Step: 3}
	r.Pre(context.Background(), in)
	r.Post(context.Background(), in, "out", true)

	var got map[string]any
	b, _ := os.ReadFile(pre)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["schema_version"] != 1.0 || got["event"] != "pre_tool" || got["tool_name"] != "bash" || got["tool_call_id"] != "c1" ||
		got["root"] != dir || got["read_only"] != false || got["step"] != 3.0 || got["arguments"].(map[string]any)["command"] != "ls" {
		t.Errorf("pre input = %s", b)
	}
	if _, ok := got["output"]; ok {
		t.Errorf("pre input has output: %s", b)
	}
	b, _ = os.ReadFile(post)
	got = nil
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "post_tool" || got["output"] != "out" || got["is_error"] != true {
		t.Errorf("post input = %s", b)
	}
}

func TestMinimalEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("POLICY_TOKEN", "tok")
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	h := hookstest.Hook(t, "", "env", out)
	h.Env = []string{"POLICY_TOKEN"}
	mustNew(t, config.Hooks{PreTool: []config.Hook{h}}).Pre(context.Background(), Input{ToolName: "bash", Root: dir})
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	env := string(b)
	if strings.Contains(env, "OPENAI_API_KEY") || !strings.Contains(env, "POLICY_TOKEN=tok") || !strings.Contains(env, "PATH=") {
		t.Errorf("hook environment = %q", env)
	}
}

func TestPreInterruptedDenies(t *testing.T) {
	h := hookstest.Hook(t, "", "sleep")
	h.OnError = Allow
	r := mustNew(t, config.Hooks{PreTool: []config.Hook{h}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := r.Pre(ctx, Input{ToolName: "bash", Root: t.TempDir()})
	if len(res) != 1 || !res[0].Denied || res[0].Decision != Error {
		t.Errorf("results = %+v", res)
	}
}
