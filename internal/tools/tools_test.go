package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
)

func TestResolveRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"..", "../x", "a/../../x", "/etc/passwd"} {
		if _, err := Resolve(root, p); err == nil {
			t.Errorf("Resolve(%q) succeeded, want error", p)
		}
	}
	for _, p := range []string{"", ".", "a/b", "a/../b", filepath.Join(root, "x")} {
		if _, err := Resolve(root, p); err != nil {
			t.Errorf("Resolve(%q): %v", p, err)
		}
	}
}

func TestWriteReadEdit(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "a/b/c.txt", "foo bar foo"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(root, "a/b/c.txt")
	if err != nil || got != "foo bar foo" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if err := EditFile(root, "a/b/c.txt", "foo", "x"); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Errorf("ambiguous edit: err = %v", err)
	}
	if err := EditFile(root, "a/b/c.txt", "nope", "x"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing edit: err = %v", err)
	}
	if err := EditFile(root, "a/b/c.txt", "bar", "baz"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadFile(root, "a/b/c.txt"); got != "foo baz foo" {
		t.Errorf("after edit = %q", got)
	}
}

func setupTree(t *testing.T) string {
	root := t.TempDir()
	for p, c := range map[string]string{
		"main.go":         "package main\nfunc main() {}\n",
		"pkg/util.go":     "package pkg\n// TODO fix\n",
		"pkg/util.txt":    "TODO later\n",
		".git/config":     "TODO hidden\n",
		"bin/data.bin":    "TODO\x00binary",
		"pkg/sub/deep.go": "package sub\n",
	} {
		if err := WriteFile(root, p, c); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestListFiles(t *testing.T) {
	root := setupTree(t)
	out, err := ListFiles(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg/") || !strings.Contains(out, "main.go") {
		t.Errorf("plain listing = %q", out)
	}
	out, _ = ListFiles(root, ".", "*.go")
	if out != "main.go\npkg/sub/deep.go\npkg/util.go" {
		t.Errorf("glob *.go = %q", out)
	}
	out, _ = ListFiles(root, "pkg", "sub/*.go")
	if out != "pkg/sub/deep.go" {
		t.Errorf("glob sub/*.go = %q", out)
	}
	if _, err := ListFiles(root, "..", ""); err == nil {
		t.Error("listing .. succeeded")
	}
}

func TestGrep(t *testing.T) {
	root := setupTree(t)
	out, err := Grep(root, "TODO", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "pkg/util.go:2: // TODO fix\npkg/util.txt:1: TODO later"
	if out != want {
		t.Errorf("Grep = %q, want %q", out, want)
	}
	if out, _ := Grep(root, "zzz", "pkg"); out != "no matches" {
		t.Errorf("no-match grep = %q", out)
	}
	if _, err := Grep(root, "(", ""); err == nil {
		t.Error("bad regexp accepted")
	}
}

func TestBash(t *testing.T) {
	root := t.TempDir()
	out, err := Bash(context.Background(), root, "echo hi; echo err >&2; pwd; exit 3", time.Second*5)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<returncode>3</returncode>", "hi", "err", root} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestBashTimeoutKillsGroup(t *testing.T) {
	root := t.TempDir()
	start := time.Now()
	// The background sleep would hold stdout open forever if only bash were killed.
	out, err := Bash(context.Background(), root, "sleep 30 & sleep 30", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "timed out") {
		t.Errorf("output = %q", out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s, process group not killed", d)
	}
}

func TestExecutorRun(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root, MaxOutputChars: 100}
	call := func(name, args string) string {
		return e.Run(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: name, Arguments: args}})
	}
	if out := call("write_file", `{"path":"x.txt","content":"`+strings.Repeat("a", 500)+`"}`); out != "ok" {
		t.Errorf("write = %q", out)
	}
	if out := call("read_file", `{"path":"x.txt"}`); !strings.Contains(out, "elided") || len(out) > 200 {
		t.Errorf("read not truncated: %d chars", len(out))
	}
	if out := call("read_file", `{"path":"../x"}`); !strings.HasPrefix(out, "error:") {
		t.Errorf("traversal = %q", out)
	}
	if out := call("nope", `{}`); !strings.Contains(out, "unknown tool") {
		t.Errorf("unknown = %q", out)
	}
	if out := call("read_file", `{bad`); !strings.Contains(out, "invalid arguments") {
		t.Errorf("bad json = %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); err != nil {
		t.Error(err)
	}
	if n := len(Specs()); n != 6 {
		t.Errorf("Specs() has %d tools", n)
	}
}
