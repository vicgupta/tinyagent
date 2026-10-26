package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// argvScript prints each argument as <arg> and leaves a ran file behind.
const argvScript = "#!/bin/sh\ntouch \"$(dirname \"$0\")/ran\"\nfor a in \"$@\"; do printf '<%s>' \"$a\"; done\n"

func fakeExe(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executables are shell scripts")
	}
	p := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

var testsParams = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"packages": map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": `^\./`}, "minItems": 1},
		"verbose":  map[string]any{"type": "boolean"},
		"count":    map[string]any{"type": "integer"},
		"raw":      map[string]any{"type": "string"},
	},
	"required":             []any{"packages"},
	"additionalProperties": false,
}

func commandTool(t *testing.T, root string, def config.CommandTool) *CommandTool {
	t.Helper()
	cts, err := NewCommandTools([]config.CommandTool{def}, root, 5)
	if err != nil {
		t.Fatal(err)
	}
	return cts[0]
}

func callTool(e *Executor, name, args string) Result {
	return e.RunResult(context.Background(), model.ToolCall{ID: "1", Type: "function",
		Function: model.FunctionCall{Name: name, Arguments: args}})
}

func TestNewCommandToolsInvalid(t *testing.T) {
	root := t.TempDir()
	ok := config.CommandTool{Name: "run_tests", Description: "d", Parameters: testsParams, Argv: []string{"go", "test", "{{packages}}"}}
	with := func(f func(*config.CommandTool)) []config.CommandTool {
		d := ok
		d.Argv = append([]string(nil), ok.Argv...)
		f(&d)
		return []config.CommandTool{d}
	}
	objParams := map[string]any{"type": "object", "properties": map[string]any{"o": map[string]any{"type": "object"}}}
	for _, c := range []struct {
		defs []config.CommandTool
		want string
	}{
		{with(func(d *config.CommandTool) { d.Name = "bash" }), "built-in"},
		{with(func(d *config.CommandTool) { d.Name = "apply_patch" }), "built-in"},
		{with(func(d *config.CommandTool) { d.Name = "submit_result" }), "built-in"},
		{with(func(d *config.CommandTool) { d.Name = "1bad" }), "name"},
		{append(with(func(*config.CommandTool) {}), ok), "another command tool"},
		{with(func(d *config.CommandTool) { d.Description = " " }), "description"},
		{with(func(d *config.CommandTool) { d.Argv = nil }), "argv must not be empty"},
		{with(func(d *config.CommandTool) { d.Argv = []string{"{{raw}}"} }), "argv[0]"},
		{with(func(d *config.CommandTool) { d.Argv = []string{"go", "{{nope}}"} }), "undeclared"},
		{with(func(d *config.CommandTool) { d.Argv = []string{"go", "--pkg={{raw}}"} }), "whole element"},
		{with(func(d *config.CommandTool) { d.Parameters = objParams; d.Argv = []string{"go", "{{o}}"} }), "object property"},
		{with(func(d *config.CommandTool) { d.Parameters = map[string]any{"type": "object", "oneOf": []any{}} }), "parameters"},
		{with(func(d *config.CommandTool) { d.Parameters = map[string]any{"type": "string"} }), "parameters"},
		{with(func(d *config.CommandTool) { d.Dir = "../x" }), "dir"},
		{with(func(d *config.CommandTool) { d.Dir = "missing" }), "dir"},
		{with(func(d *config.CommandTool) { d.TimeoutSec = 601 }), "timeout_seconds"},
	} {
		_, err := NewCommandTools(c.defs, root, 120)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "command_tools[") {
			t.Errorf("%+v: err = %v, want %q", c.defs[len(c.defs)-1], err, c.want)
		}
	}
	if _, err := NewCommandTools([]config.CommandTool{ok}, root, 120); err != nil {
		t.Fatal(err)
	}
}

func TestCommandToolExpand(t *testing.T) {
	c := commandTool(t, t.TempDir(), config.CommandTool{Name: "x", Description: "d", Parameters: testsParams,
		Argv: []string{"go", "test", "-count=1", "{{packages}}", "{{verbose}}", "{{count}}", "{{raw}}"}})
	argv, errs := c.Expand(`{"packages": ["./a", "./b"]}`)
	if errs != nil || strings.Join(argv, "|") != "go|test|-count=1|./a|./b" {
		t.Errorf("argv = %q, %v", argv, errs)
	}
	argv, _ = c.Expand(`{"packages": ["./a"], "verbose": true, "count": 3, "raw": "a b"}`)
	if strings.Join(argv, "|") != "go|test|-count=1|./a|true|3|a b" {
		t.Errorf("argv = %q", argv)
	}
	_, errs = c.Expand(`{"packages": ["/etc"], "extra": 1}`)
	if len(errs) != 2 {
		t.Errorf("errs = %q", errs)
	}
	if _, errs = c.Expand(`{}`); len(errs) != 1 {
		t.Errorf("errs = %q", errs)
	}
}

// commandEnvs are the environments a command tool must run the same way in.
func commandEnvs(t *testing.T, root string) map[string]Environment {
	envs := map[string]Environment{"local": Local{}, "command_env": Local{CommandEnv: NewCommandEnv(config.CommandEnv{Inherit: "core"}, os.Environ())}}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if sb, err := NewSandbox(root, nil, false, nil); err == nil {
			t.Cleanup(sb.Cleanup)
			envs["sandbox"] = Local{Sandbox: sb}
		}
	}
	return envs
}

func TestCommandToolArgvNoShell(t *testing.T) {
	exe := fakeExe(t, argvScript)
	root := t.TempDir()
	c := commandTool(t, root, config.CommandTool{Name: "echo_args", Description: "d", Parameters: testsParams,
		Argv: []string{exe, "lit eral", "{{raw}}", "{{packages}}"}})
	nasty := "; rm -rf ~ $(id) `id` \n next"
	for name, env := range commandEnvs(t, root) {
		e := &Executor{Root: root, Env: env, CommandTools: []*CommandTool{c}}
		r := callTool(e, "echo_args", `{"raw": "`+strings.ReplaceAll(nasty, "\n", `\n`)+`", "packages": ["./a b", "./$HOME"]}`)
		want := "<lit eral><" + nasty + "><./a b><./$HOME>"
		if r.IsError || !strings.Contains(r.Text, "<returncode>0</returncode>") || !strings.Contains(r.Text, want) {
			t.Errorf("%s: %v %q, want %q", name, r.IsError, r.Text, want)
		}
	}
}

func TestCommandToolContainer(t *testing.T) {
	c, root := fakeContainer(t)
	exe := fakeExe(t, argvScript)
	ct := commandTool(t, root, config.CommandTool{Name: "echo_args", Description: "d", Parameters: testsParams,
		Argv: []string{exe, "{{raw}}", "{{packages}}"}})
	e := &Executor{Root: root, Env: c, CommandTools: []*CommandTool{ct}}
	r := callTool(e, "echo_args", `{"raw": "$(id); x", "packages": ["./a"]}`)
	if r.IsError || !strings.Contains(r.Text, "<$(id); x><./a>") {
		t.Errorf("%q", r.Text)
	}
}

func TestCommandToolSchemaRejects(t *testing.T) {
	exe := fakeExe(t, argvScript)
	root := t.TempDir()
	c := commandTool(t, root, config.CommandTool{Name: "x", Description: "d", Parameters: testsParams, Argv: []string{exe, "{{packages}}"}})
	e := &Executor{Root: root, CommandTools: []*CommandTool{c}}
	for _, args := range []string{`{}`, `{"packages": ["./a"], "zz": 1}`, `{"packages": ["-rf"]}`, `not json`} {
		r := callTool(e, "x", args)
		if !r.IsError || !strings.Contains(r.Text, "nothing ran") {
			t.Errorf("%s: %v %q", args, r.IsError, r.Text)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "ran")); err == nil {
		t.Error("the executable ran")
	}
}

func TestCommandToolRunBehavior(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix commands")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	defs := []config.CommandTool{
		{Name: "slow", Description: "d", Argv: []string{"sleep", "10"}, TimeoutSec: 1},
		{Name: "big", Description: "d", Argv: []string{"head", "-c", "5000", "/dev/zero"}},
		{Name: "missing", Description: "d", Argv: []string{"no-such-exe-xyz"}},
		{Name: "fails", Description: "d", Argv: []string{"false"}},
		{Name: "where", Description: "d", Argv: []string{"pwd"}, Dir: "sub"},
		{Name: "env", Description: "d", Argv: []string{"/usr/bin/env"}},
	}
	cts, err := NewCommandTools(defs, root, 5)
	if err != nil {
		t.Fatal(err)
	}
	cmdEnv := NewCommandEnv(config.CommandEnv{Inherit: "none", Set: map[string]string{"CI": "true", "PATH": os.Getenv("PATH")}}, os.Environ())
	e := &Executor{Root: root, CommandTools: cts, MaxOutputChars: 1000, Env: Local{CommandEnv: cmdEnv}}

	start := time.Now()
	if r := callTool(e, "slow", ""); !strings.Contains(r.Text, "timed out after 1s") || time.Since(start) > 5*time.Second {
		t.Errorf("slow: %q after %s", r.Text, time.Since(start))
	}
	if r := callTool(e, "big", "{}"); !strings.Contains(r.Text, "characters elided") {
		t.Errorf("big not truncated: %d chars", len(r.Text))
	}
	if r := callTool(e, "missing", "{}"); !r.IsError || !strings.Contains(r.Text, "no-such-exe-xyz") || !strings.Contains(r.Text, "local environment") {
		t.Errorf("missing: %v %q", r.IsError, r.Text)
	}
	if r := callTool(e, "fails", "{}"); r.IsError || !strings.Contains(r.Text, "<returncode>1</returncode>") {
		t.Errorf("fails: %v %q", r.IsError, r.Text)
	}
	if r := callTool(e, "where", "{}"); !strings.Contains(r.Text, filepath.Join(root, "sub")) {
		t.Errorf("where: %q", r.Text)
	}
	// The command_env policy applies as for bash (which adds only what bash
	// itself sets).
	want := "<returncode>0</returncode>\n<output>\nCI=true\nPATH=" + os.Getenv("PATH") + "\n</output>"
	if r := callTool(e, "env", "{}"); r.Text != want {
		t.Errorf("env: %q", r.Text)
	}
	if r := callTool(e, "bash", `{"command": "/usr/bin/env -u PWD -u SHLVL -u _ | sort"}`); r.Text != want {
		t.Errorf("bash env: %q", r.Text)
	}

	// Without PATH in command_env, a bare name is not found.
	e.Env = Local{CommandEnv: NewCommandEnv(config.CommandEnv{Inherit: "none"}, os.Environ())}
	if r := callTool(e, "fails", "{}"); !r.IsError || !strings.Contains(r.Text, "PATH") {
		t.Errorf("no PATH: %q", r.Text)
	}
}

func TestCommandToolSandbox(t *testing.T) {
	root := t.TempDir()
	outside, err := os.MkdirTemp("/var/tmp", "tinyagent-outside-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(outside)
	sb := sandboxOrSkip(t, root, nil, false)
	probe := filepath.Join(outside, "probe")
	cts, err := NewCommandTools([]config.CommandTool{{Name: "touch", Description: "d", Argv: []string{"touch", probe}}}, root, 5)
	if err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, CommandTools: cts, Env: Local{Sandbox: sb}}
	if r := callTool(e, "touch", "{}"); strings.Contains(r.Text, "<returncode>0</returncode>") {
		t.Errorf("write outside the sandbox succeeded: %q", r.Text)
	}
	if _, err := os.Stat(probe); err == nil {
		t.Error("probe was created")
	}
}

func TestCommandToolReadOnly(t *testing.T) {
	exe := fakeExe(t, argvScript)
	root := t.TempDir()
	c := commandTool(t, root, config.CommandTool{Name: "x", Description: "d", Argv: []string{exe}})
	e := &Executor{Root: root, CommandTools: []*CommandTool{c}}
	if !e.Permits("x") {
		t.Error("not offered")
	}
	e.ReadOnly = true
	if e.Permits("x") {
		t.Error("offered in read-only mode")
	}
	if r := callTool(e, "x", "{}"); !r.IsError || !strings.Contains(r.Text, "read-only") {
		t.Errorf("%q", r.Text)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "ran")); err == nil {
		t.Error("the executable ran")
	}
}
