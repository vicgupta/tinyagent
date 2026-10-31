package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupHintKnownFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"load error with key", errors.New(`config /c.yaml: permissions.rules[2]: decision must be allow, ask or deny, got "x"`), "fix permissions.rules[2] in /c.yaml, then rerun tinyagent --check"},
		{"lsp server", errors.New(`config /c.yaml: lsp.servers[0] (x): command is required`), "fix lsp.servers[0] in /c.yaml"},
		{"yaml parse", errors.New("parse config /c.yaml: yaml: line 1: bad"), "fix /c.yaml, then rerun tinyagent --check"},
		{"hooks after load", inConfig("/c.yaml", errors.New("hooks.post_tool[1]: command is required")), "fix hooks.post_tool[1] in /c.yaml"},
		{"command tool", inConfig("/c.yaml", fmt.Errorf("config: %w", errors.New("command_tools[0] (lint): name is reserved"))), "fix command_tools[0] in /c.yaml"},
		{"sandbox bwrap", sandboxUnavailable("bwrap", errors.New("sandbox: bwrap not found")), "the bwrap sandbox backend can't start on this host"},
		{"sandbox auto", sandboxUnavailable("", errors.New("sandbox: unsupported")), "try --sandbox-backend landlock, or run without --sandbox"},
		{"sandbox landlock", sandboxUnavailable("landlock", errors.New("x")), "try --sandbox-backend bwrap"},
	} {
		if got := setupHint(tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("%s: setupHint = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSetupHintUnknownErrorHasNone(t *testing.T) {
	for _, err := range []error{nil, errors.New("boom"), errors.New("config: no file named"), fmt.Errorf("read config: %w", os.ErrNotExist)} {
		if h := setupHint(err); h != "" {
			t.Errorf("setupHint(%v) = %q, want none", err, h)
		}
	}
}

func TestConfigFileErrorNamesPathOnce(t *testing.T) {
	err := inConfig("/c.yaml", errors.New("permissions.rules[0]: bad"))
	if err.Error() != "config /c.yaml: permissions.rules[0]: bad" {
		t.Fatalf("got %q", err)
	}
	if got := inConfig("/c.yaml", errors.New("config /c.yaml: x: y")).Error(); got != "config /c.yaml: x: y" {
		t.Fatalf("got %q", got)
	}
	if got := inConfig("/c.yaml", fmt.Errorf("config: %w", errors.New("a: b"))).Error(); got != "config /c.yaml: a: b" {
		t.Fatalf("got %q", got)
	}
	if inner := errors.New("x"); inConfig("", inner) != inner {
		t.Fatal("no path should leave the error unchanged")
	}
}

func TestCheckPrintsConfigHint(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"permissions.rules[0]": "permissions:\n  rules:\n    - tool: bash\n      decision: maybe\n",
		"lsp.servers[0]":       "lsp:\n  servers:\n    - name: x\n",
	}
	for key, body := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(key, "[0]", "")+".yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runCLI(t, "--check", "-c", path, "-m", "ollama/x")
		if code == 0 || !strings.Contains(stderr, "error: config "+path) {
			t.Fatalf("%s: code %d stderr %q", key, code, stderr)
		}
		if want := "hint: fix " + key + " in " + path + ", then rerun tinyagent --check"; !strings.Contains(stderr, want) {
			t.Fatalf("%s: stderr %q, want %q", key, stderr, want)
		}
	}
}

func TestCheckMissingKeyAndHandoffKeepTheirHints(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	code, _, stderr := runCLI(t, "--check", "-m", "openai/gpt-4o")
	if code == 0 || !strings.Contains(stderr, "OPENAI_API_KEY") || strings.Count(stderr, "hint:") != 1 {
		t.Fatalf("missing key: code %d stderr %q", code, stderr)
	}
	t.Setenv("PATH", t.TempDir())
	for _, spec := range []string{"claude-code/default", "codex-exec/default"} {
		code, _, stderr := runCLI(t, "--check", "-m", spec)
		if code == 0 || !strings.Contains(stderr, "not found on PATH") || strings.Count(stderr, "hint:") != 1 {
			t.Fatalf("%s: code %d stderr %q", spec, code, stderr)
		}
	}
}

func TestCheckJSONCarriesSetupHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("lsp:\n  servers:\n    - name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI(t, "--check", "--json", "-c", path, "-m", "ollama/x")
	if code != 2 || !strings.Contains(stdout, `"hint": "fix lsp.servers[0] in `+path) {
		t.Fatalf("code %d stdout %q", code, stdout)
	}
}
