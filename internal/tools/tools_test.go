package tools

import (
	"context"
	"fmt"
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
	got, err := ReadFile(root, "a/b/c.txt", 0, 0)
	if err != nil || got != "foo bar foo" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if _, err := EditFile(root, "a/b/c.txt", "foo", "x", false); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Errorf("ambiguous edit: err = %v", err)
	}
	if _, err := EditFile(root, "a/b/c.txt", "nope", "x", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing edit: err = %v", err)
	}
	if _, err := EditFile(root, "a/b/c.txt", "bar", "baz", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadFile(root, "a/b/c.txt", 0, 0); got != "foo baz foo" {
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

func TestReadFileRange(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "f.txt", "l1\nl2\nl3\nl4\nl5\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "nonl.txt", "a\nb\nc"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "empty.txt", ""); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path          string
		offset, limit int
		want          string
	}{
		{"f.txt", 0, 0, "l1\nl2\nl3\nl4\nl5\n"},
		{"f.txt", 1, 0, "l1\nl2\nl3\nl4\nl5\n"},
		{"f.txt", 1, 5, "l1\nl2\nl3\nl4\nl5\n"},
		{"f.txt", 1, 99, "l1\nl2\nl3\nl4\nl5\n"},
		{"f.txt", 2, 2, "l2\nl3\n[lines 2-3 of 5]"},
		{"f.txt", 4, 0, "l4\nl5\n[lines 4-5 of 5]"},
		{"f.txt", 0, 2, "l1\nl2\n[lines 1-2 of 5]"},
		{"f.txt", 5, 1, "l5\n[lines 5-5 of 5]"},
		{"nonl.txt", 3, 0, "c\n[lines 3-3 of 3]"},
		{"nonl.txt", 0, 0, "a\nb\nc"},
		{"empty.txt", 0, 0, ""},
		{"empty.txt", 1, 10, ""},
	}
	for _, c := range cases {
		got, err := ReadFile(root, c.path, c.offset, c.limit)
		if err != nil || got != c.want {
			t.Errorf("ReadFile(%s, %d, %d) = %q, %v; want %q", c.path, c.offset, c.limit, got, err, c.want)
		}
	}
	for _, c := range []struct {
		offset, limit int
		want          string
	}{
		{6, 0, "past the end"},
		{-1, 0, "offset"},
		{0, -1, "limit"},
	} {
		if _, err := ReadFile(root, "f.txt", c.offset, c.limit); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("ReadFile(f.txt, %d, %d) err = %v, want %q", c.offset, c.limit, err, c.want)
		}
	}
	if _, err := ReadFile(root, "empty.txt", 2, 0); err == nil {
		t.Error("offset 2 on an empty file: want an error")
	}
}

func TestExecutorReadFileRangeArgs(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := WriteFile(root, "src/main.go", b.String()); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root}
	call := func(args string) string {
		return e.Run(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "read_file", Arguments: args}})
	}
	want := ""
	for i := 100; i < 150; i++ {
		want += fmt.Sprintf("line %d\n", i)
	}
	want += "[lines 100-149 of 200]"
	// The issue's example, as JSON numbers and as the strings some models send.
	for _, args := range []string{`{"path": "src/main.go", "offset": 100, "limit": 50}`, `{"path":"src/main.go","offset":"100","limit":"50"}`} {
		if got := call(args); got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", args, got, want)
		}
	}
	if got := call(`{"path":"src/main.go"}`); got != b.String() {
		t.Errorf("no range: got %d chars, want the whole file (%d)", len(got), b.Len())
	}
	for _, args := range []string{`{"path":"src/main.go","offset":"ten"}`, `{"path":"src/main.go","offset":1.5}`, `{"path":"src/main.go","offset":-3}`} {
		if got := call(args); !strings.HasPrefix(got, "error:") {
			t.Errorf("%s = %q, want an error", args, got)
		}
	}
}

func TestReadFileSpec(t *testing.T) {
	for _, s := range Specs() {
		if s.Function.Name != "read_file" {
			continue
		}
		p := s.Function.Parameters
		props := p["properties"].(map[string]any)
		for _, k := range []string{"offset", "limit"} {
			prop, ok := props[k].(map[string]any)
			if !ok || prop["type"] != "integer" {
				t.Errorf("read_file %s = %v, want an integer property", k, props[k])
			}
		}
		if req := p["required"].([]string); len(req) != 1 || req[0] != "path" {
			t.Errorf("read_file required = %v, want [path]", req)
		}
		return
	}
	t.Fatal("no read_file spec")
}

func TestExecutorEditReplaceAll(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "x.go", "foo := 1\nbar(foo)\nfoo++\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "x.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root}
	call := func(args string) string {
		return e.Run(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "edit_file", Arguments: args}})
	}
	read := func() string {
		got, _ := ReadFile(root, "x.go", 0, 0)
		return got
	}
	if out := call(`{"path":"x.go","old_str":"foo","new_str":"n"}`); !strings.Contains(out, "occurs 3 times") {
		t.Errorf("repeated without replace_all = %q", out)
	}
	if out := call(`{"path":"x.go","old_str":"foo","new_str":"n","replace_all":false}`); !strings.Contains(out, "occurs 3 times") {
		t.Errorf("repeated with replace_all false = %q", out)
	}
	if out := call(`{"path":"x.go","old_str":"nope","new_str":"n","replace_all":true}`); !strings.Contains(out, "not found") {
		t.Errorf("missing with replace_all = %q", out)
	}
	if out := call(`{"path":"x.go","old_str":"","new_str":"n","replace_all":true}`); !strings.Contains(out, "must not be empty") {
		t.Errorf("empty old_str with replace_all = %q", out)
	}
	if out := call(`{"path":"x.go","old_str":"foo","new_str":"n","replace_all":"yes"}`); !strings.Contains(out, "replace_all must be true or false") {
		t.Errorf("non-boolean replace_all = %q", out)
	}
	if got := read(); got != "foo := 1\nbar(foo)\nfoo++\n" {
		t.Errorf("failed edits changed the file: %q", got)
	}
	if out := call(`{"path":"x.go","old_str":"foo","new_str":"n","replace_all":true}`); out != "ok (3 replacements)" {
		t.Errorf("replace_all = %q", out)
	}
	if got := read(); got != "n := 1\nbar(n)\nn++\n" {
		t.Errorf("after replace_all = %q", got)
	}
	if out := call(`{"path":"x.go","old_str":"bar","new_str":"baz"}`); out != "ok" {
		t.Errorf("unique edit = %q", out)
	}
	if fi, err := os.Stat(filepath.Join(root, "x.go")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode after edits = %v, %v", fi.Mode().Perm(), err)
	}
}

func TestEditFileSpec(t *testing.T) {
	for _, s := range Specs() {
		if s.Function.Name != "edit_file" {
			continue
		}
		p := s.Function.Parameters
		props := p["properties"].(map[string]any)
		if prop, ok := props["replace_all"].(map[string]any); !ok || prop["type"] != "boolean" {
			t.Errorf("edit_file replace_all = %v, want a boolean property", props["replace_all"])
		}
		for _, r := range p["required"].([]string) {
			if r == "replace_all" {
				t.Error("replace_all is required")
			}
		}
		if !strings.Contains(s.Function.Description, "replace_all") {
			t.Errorf("edit_file description doesn't mention replace_all: %q", s.Function.Description)
		}
		return
	}
	t.Fatal("no edit_file spec")
}
