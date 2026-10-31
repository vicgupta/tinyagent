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

func TestReadOnlyExecutorDeniesNonReadTools(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	e := &Executor{Root: root, ReadOnly: true}
	for _, tc := range []struct{ name, args string }{
		{"write_file", `{"path":"marker","content":"written"}`},
		{"edit_file", `{"path":"marker","old_str":"x","new_str":"y"}`},
		{"bash", `{"command":"touch marker"}`},
		{"start_process", `{"command":"touch marker"}`},
		{"read_process", `{}`}, {"stop_process", `{}`}, {"future_tool", `{}`},
	} {
		out := e.RunResult(context.Background(), model.ToolCall{ID: "id", Function: model.FunctionCall{Name: tc.name, Arguments: tc.args}}).Text
		if !strings.Contains(out, "read-only mode") {
			t.Errorf("%s: %q", tc.name, out)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker exists or stat failed: %v", err)
	}
	specs := Specs(true)
	want := []string{"read_file", "list_files", "grep"}
	if len(specs) != len(want) {
		t.Fatalf("specs=%v", specs)
	}
	for i, spec := range specs {
		if spec.Function.Name != want[i] {
			t.Errorf("spec %d=%q", i, spec.Function.Name)
		}
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
	out, err := Grep(root, "TODO", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "pkg/util.go:2: // TODO fix\npkg/util.txt:1: TODO later"
	if out != want {
		t.Errorf("Grep = %q, want %q", out, want)
	}
	if out, _ := Grep(root, "zzz", "pkg", "", false, false); out != "no matches" {
		t.Errorf("no-match grep = %q", out)
	}
	if _, err := Grep(root, "(", "", "", false, false); err == nil {
		t.Error("bad regexp accepted")
	}
}

func TestGrepInclude(t *testing.T) {
	root := setupTree(t)
	if out, err := Grep(root, "TODO", "", "*.go", false, false); err != nil || out != "pkg/util.go:2: // TODO fix" {
		t.Errorf("include *.go = %q, %v", out, err)
	}
	if out, _ := Grep(root, "TODO", "", "*.txt", false, false); out != "pkg/util.txt:1: TODO later" {
		t.Errorf("include *.txt = %q", out)
	}
	if out, _ := Grep(root, "package", "", "pkg/*/*.go", false, false); out != "pkg/sub/deep.go:1: package sub" {
		t.Errorf("include pkg/*/*.go = %q", out)
	}
	if out, _ := Grep(root, "package", "pkg", "sub/*.go", false, false); out != "pkg/sub/deep.go:1: package sub" {
		t.Errorf("include sub/*.go under pkg = %q", out)
	}
	if out, _ := Grep(root, "TODO", "", "*.md", false, false); out != "no matches" {
		t.Errorf("include *.md = %q", out)
	}
	if _, err := Grep(root, "TODO", "", "[", false, false); err == nil || !strings.Contains(err.Error(), `bad include pattern "["`) {
		t.Errorf("bad include err = %v", err)
	}
	// A single-file path ignores include.
	if out, _ := Grep(root, "TODO", "pkg/util.txt", "*.go", false, false); out != "pkg/util.txt:1: TODO later" {
		t.Errorf("single file with include = %q", out)
	}
	all, _ := Grep(root, "TODO", "", "", false, false)
	if out, _ := Grep(root, "TODO", "", "", false, false); out != all {
		t.Errorf("empty include changed results: %q", out)
	}
	e := &Executor{Root: root}
	out := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: `{"pattern":"TODO","include":"*.go"}`}}).Text
	if out != "pkg/util.go:2: // TODO fix" {
		t.Errorf("executor grep include = %q", out)
	}
}

func TestGrepSpec(t *testing.T) {
	for _, s := range Specs() {
		if s.Function.Name != "grep" {
			continue
		}
		p := s.Function.Parameters
		props := p["properties"].(map[string]any)
		if prop, ok := props["include"].(map[string]any); !ok || prop["type"] != "string" {
			t.Errorf("grep include = %v, want a string property", props["include"])
		}
		if prop, ok := props["context_lines"].(map[string]any); !ok || prop["type"] != "integer" || !strings.Contains(prop["description"].(string), "0 to 20") {
			t.Errorf("grep context_lines = %v, want optional integer range description", props["context_lines"])
		}
		for _, r := range p["required"].([]string) {
			if r == "include" {
				t.Error("include is required")
			}
		}
		return
	}
	t.Fatal("no grep spec")
}

func TestExecutorGrepIgnoreCase(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"Logs/app.log":     "Error: connection failed\nERROR: timeout\nerror: retrying\n",
		"Logs/other.LOG":   "ERROR: excluded by glob\n",
		"Logs/notes.txt":   "ERROR: excluded by extension\n",
		"Logs/sub/api.log": "eRrOr: nested\n",
		".git/hidden.log":  "ERROR: hidden\n",
		"Logs/binary.log":  "ERROR\x00binary\n",
	} {
		if err := WriteFile(root, path, content); err != nil {
			t.Fatal(err)
		}
	}
	e := &Executor{Root: root}
	all := "Logs/app.log:1: Error: connection failed\nLogs/app.log:2: ERROR: timeout\nLogs/app.log:3: error: retrying"
	for _, tt := range []struct {
		name string
		args string
		want string
	}{
		{"omitted", `{"pattern":"error","path":"Logs/app.log"}`, "Logs/app.log:3: error: retrying"},
		{"false", `{"pattern":"error","path":"Logs/app.log","ignore_case":false}`, "Logs/app.log:3: error: retrying"},
		{"null", `{"pattern":"error","path":"Logs/app.log","ignore_case":null}`, "Logs/app.log:3: error: retrying"},
		{"single file", `{"pattern":"error","path":"Logs/app.log","ignore_case":true}`, all},
		{"single file ignores include", `{"pattern":"error","path":"Logs/app.log","include":"*.txt","ignore_case":true}`, all},
		{"recursive", `{"pattern":"error","ignore_case":true}`, all + "\nLogs/notes.txt:1: ERROR: excluded by extension\nLogs/other.LOG:1: ERROR: excluded by glob\nLogs/sub/api.log:1: eRrOr: nested"},
		{"recursive include", `{"pattern":"error","include":"*.log","ignore_case":true}`, all + "\nLogs/sub/api.log:1: eRrOr: nested"},
		{"relative include", `{"pattern":"error","path":"Logs","include":"sub/*.log","ignore_case":true}`, "Logs/sub/api.log:1: eRrOr: nested"},
		{"include stays case sensitive", `{"pattern":"error","include":"logs/*.log","ignore_case":true}`, "no matches"},
		{"no matches", `{"pattern":"missing","ignore_case":true}`, "no matches"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: tt.args}}).Text
			if got != tt.want {
				t.Errorf("grep %s = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
	for _, flag := range []string{"", `,"ignore_case":false`, `,"ignore_case":null`, `,"ignore_case":true`} {
		args := `{"pattern":"(?i)error","path":"Logs/app.log"` + flag + `}`
		if got := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: args}}).Text; got != all {
			t.Errorf("inline flag with %s = %q, want %q", args, got, all)
		}
	}
}

func TestExecutorGrepIgnoreCaseValidation(t *testing.T) {
	e := &Executor{Root: t.TempDir()}
	for _, value := range []string{`"true"`, `"yes"`, `0`, `1`, `[]`, `{}`} {
		args := `{"pattern":"error","ignore_case":` + value + `}`
		want := "error: ignore_case must be true or false, got " + value
		if got := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: args}}).Text; got != want {
			t.Errorf("grep %s = %q, want %q", args, got, want)
		}
	}
	for _, flag := range []string{"", `,"ignore_case":false`, `,"ignore_case":null`, `,"ignore_case":true`} {
		args := `{"pattern":"("` + flag + `}`
		if got := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: args}}).Text; !strings.HasPrefix(got, "error: bad regexp:") {
			t.Errorf("grep %s = %q, want a bad regexp error", args, got)
		}
	}
}

func TestGrepIgnoreCaseSpec(t *testing.T) {
	for _, s := range Specs() {
		if s.Function.Name != "grep" {
			continue
		}
		props := s.Function.Parameters["properties"].(map[string]any)
		prop, ok := props["ignore_case"].(map[string]any)
		if !ok || prop["type"] != "boolean" {
			t.Fatalf("grep ignore_case = %v, want a boolean property", props["ignore_case"])
		}
		if desc, _ := prop["description"].(string); !strings.Contains(desc, "default false") {
			t.Errorf("ignore_case description = %q, want default false", desc)
		}
		for _, required := range s.Function.Parameters["required"].([]string) {
			if required == "ignore_case" {
				t.Error("ignore_case is required")
			}
		}
		return
	}
	t.Fatal("no grep spec")
}

func TestGrepIgnoreCaseLimits(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "large.log", strings.Repeat("ERROR\n", maxGrepFileSize/6+1)); err != nil {
		t.Fatal(err)
	}
	if got, err := Grep(root, "error", "", "", true, false); err != nil || got != "no matches" {
		t.Fatalf("large file = %q, %v", got, err)
	}
	if err := WriteFile(root, "matches.log", strings.Repeat("ERROR\n", maxGrepMatches+1)); err != nil {
		t.Fatal(err)
	}
	for _, fixedStrings := range []bool{false, true} {
		got, err := Grep(root, "error", "", "", true, fixedStrings)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(got, "\n")
		if len(lines) != maxGrepMatches+1 || lines[0] != "matches.log:1: ERROR" ||
			lines[maxGrepMatches-1] != fmt.Sprintf("matches.log:%d: ERROR", maxGrepMatches) ||
			lines[maxGrepMatches] != fmt.Sprintf("... (stopped at %d matches)", maxGrepMatches) {
			t.Errorf("match limit (fixed_strings=%t): got %d lines, ending in %q", fixedStrings, len(lines), lines[len(lines)-1])
		}
	}
}

func TestExecutorGrepFixedStrings(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"main.go":        "config.json\nconfigXjson\nfunc(\n[\n*\n\\\n(?i)config.json\nCONFIG.JSON\n",
		"pkg/util.go":    "func(\n",
		"pkg/util.txt":   "func(\n",
		".git/hidden.go": "func(\n",
		"pkg/binary.go":  "func(\x00binary\n",
		"pkg/large.go":   strings.Repeat("func(\n", maxGrepFileSize/6+1),
	} {
		if err := WriteFile(root, path, content); err != nil {
			t.Fatal(err)
		}
	}
	e := &Executor{Root: root}
	for _, tt := range []struct {
		name string
		args string
		want string
	}{
		{"dot", `{"pattern":"config.json","path":"main.go","fixed_strings":true}`, "main.go:1: config.json\nmain.go:7: (?i)config.json"},
		{"parenthesis", `{"pattern":"func(","path":"main.go","fixed_strings":true}`, "main.go:3: func("},
		{"bracket", `{"pattern":"[","path":"main.go","fixed_strings":true}`, "main.go:4: ["},
		{"asterisk", `{"pattern":"*","path":"main.go","fixed_strings":true}`, "main.go:5: *"},
		{"backslash", `{"pattern":"\\","path":"main.go","fixed_strings":true}`, "main.go:6: \\"},
		{"inline flags are literal", `{"pattern":"(?i)config.json","path":"main.go","fixed_strings":true}`, "main.go:7: (?i)config.json"},
		{"ignore case", `{"pattern":"config.json","path":"main.go","fixed_strings":true,"ignore_case":true}`, "main.go:1: config.json\nmain.go:7: (?i)config.json\nmain.go:8: CONFIG.JSON"},
		{"single file ignores include", `{"pattern":"func(","path":"main.go","include":"*.txt","fixed_strings":true}`, "main.go:3: func("},
		{"recursive", `{"pattern":"func(","fixed_strings":true}`, "main.go:3: func(\npkg/util.go:1: func(\npkg/util.txt:1: func("},
		{"recursive include", `{"pattern":"func(","include":"*.go","fixed_strings":true}`, "main.go:3: func(\npkg/util.go:1: func("},
		{"relative include", `{"pattern":"func(","path":"pkg","include":"*.go","fixed_strings":true}`, "pkg/util.go:1: func("},
		{"path include", `{"pattern":"func(","include":"pkg/*.go","fixed_strings":true}`, "pkg/util.go:1: func("},
		{"no matches", `{"pattern":"missing(","fixed_strings":true}`, "no matches"},
		{"include excludes matches", `{"pattern":"func(","include":"*.md","fixed_strings":true}`, "no matches"},
		{"path confinement", `{"pattern":"func(","path":"..","fixed_strings":true}`, `error: path ".." is outside the working directory`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: tt.args}}).Text
			if got != tt.want {
				t.Errorf("grep %s = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
	for _, flag := range []string{"", `,"fixed_strings":false`, `,"fixed_strings":null`} {
		for _, pattern := range []string{"config.json", "(?i)config.json", "func("} {
			args := `{"pattern":` + fmt.Sprintf("%q", pattern) + `,"path":"main.go"` + flag + `}`
			got := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: args}}).Text
			want := "main.go:1: config.json\nmain.go:2: configXjson\nmain.go:7: (?i)config.json"
			if pattern == "(?i)config.json" {
				want += "\nmain.go:8: CONFIG.JSON"
			}
			if pattern == "func(" {
				if !strings.HasPrefix(got, "error: bad regexp:") {
					t.Errorf("grep %s = %q, want a bad regexp error", args, got)
				}
			} else if got != want {
				t.Errorf("grep %s = %q, want %q", args, got, want)
			}
		}
	}
}

func TestExecutorGrepFixedStringsValidation(t *testing.T) {
	e := &Executor{Root: t.TempDir()}
	for _, value := range []string{`"true"`, `"yes"`, `0`, `1`, `[]`, `{}`} {
		args := `{"pattern":"func(","fixed_strings":` + value + `}`
		want := "error: fixed_strings must be true or false, got " + value
		if got := e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: args}}).Text; got != want {
			t.Errorf("grep %s = %q, want %q", args, got, want)
		}
	}
}

func TestGrepFixedStringsSpec(t *testing.T) {
	for _, s := range Specs() {
		if s.Function.Name != "grep" {
			continue
		}
		props := s.Function.Parameters["properties"].(map[string]any)
		prop, ok := props["fixed_strings"].(map[string]any)
		if !ok || prop["type"] != "boolean" {
			t.Fatalf("grep fixed_strings = %v, want a boolean property", props["fixed_strings"])
		}
		if desc, _ := prop["description"].(string); !strings.Contains(desc, "default false") {
			t.Errorf("fixed_strings description = %q, want default false", desc)
		}
		if !strings.Contains(s.Function.Description, "fixed_strings") {
			t.Errorf("grep description = %q, want fixed_strings guidance", s.Function.Description)
		}
		pattern := props["pattern"].(map[string]any)
		if desc, _ := pattern["description"].(string); !strings.Contains(desc, "fixed_strings") {
			t.Errorf("pattern description = %q, want fixed_strings guidance", desc)
		}
		for _, required := range s.Function.Parameters["required"].([]string) {
			if required == "fixed_strings" {
				t.Error("fixed_strings is required")
			}
		}
		return
	}
	t.Fatal("no grep spec")
}

func TestExecutorGrepContext(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "src/a.go", "one\ntwo\nHIT\nfour\nfive\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "src/b.go", "HIT\nlast"); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root}
	call := func(args string) string {
		return e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "grep", Arguments: args}}).Text
	}
	if got := call(`{"pattern":"HIT","path":"src/a.go","context_lines":1}`); got != "src/a.go-2- two\nsrc/a.go:3: HIT\nsrc/a.go-4- four" {
		t.Errorf("middle context = %q", got)
	}
	if got := call(`{"pattern":"HIT","path":"src","context_lines":1}`); got != "src/a.go-2- two\nsrc/a.go:3: HIT\nsrc/a.go-4- four\n--\nsrc/b.go:1: HIT\nsrc/b.go-2- last" {
		t.Errorf("file boundaries = %q", got)
	}
	if got := call(`{"pattern":"HIT","path":"src/a.go","context_lines":0}`); got != "src/a.go:3: HIT" {
		t.Errorf("zero context = %q", got)
	}
	for _, val := range []string{"-1", "21", "1.5", `"bad"`, "9223372036854775808"} {
		if got := call(`{"pattern":"HIT","context_lines":` + val + `}`); !strings.HasPrefix(got, "error: context_lines must") {
			t.Errorf("invalid context %s = %q", val, got)
		}
	}
	for _, val := range []string{"0", "20", `"2"`, "null"} {
		if got := call(`{"pattern":"HIT","context_lines":` + val + `}`); strings.HasPrefix(got, "error:") {
			t.Errorf("valid context %s = %q", val, got)
		}
	}
}

func TestGrepContextMergesAndCaps(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "dense.txt", "a\nHIT\nHIT\nHIT\nz\n"); err != nil {
		t.Fatal(err)
	}
	got, err := GrepWithContext(root, "HIT", "dense.txt", "", false, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "dense.txt-1- a\ndense.txt:2: HIT\ndense.txt:3: HIT\ndense.txt:4: HIT\ndense.txt-5- z" {
		t.Errorf("merged context = %q", got)
	}
	if err := WriteFile(root, "many.txt", strings.Repeat("HIT\n", 1000)); err != nil {
		t.Fatal(err)
	}
	got, err = GrepWithContext(root, "HIT", "many.txt", "", false, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "results/context may be incomplete") {
		t.Errorf("context budget notice missing")
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
		return e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: name, Arguments: args}}).Text
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
	if n := len(Specs()); n != 10 {
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

func TestExecutorReadFileBinaryAndText(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"binary.bin":      "header\x00contents",
		"later-null.bin":  strings.Repeat("a", 7999) + "\x00contents",
		"after-limit.bin": strings.Repeat("a", 8000) + "\x00contents",
		"text.txt":        "plain text\n",
		"utf8.txt":        "café 世界\n",
		"empty.txt":       "",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &Executor{Root: root}
	call := func(args string) string {
		return e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "read_file", Arguments: args}}).Text
	}
	for _, tc := range []struct {
		name, args, want string
	}{
		{"binary without range", `{"path":"binary.bin"}`, "error: binary.bin looks like a binary file (15 bytes); use bash (e.g. file, xxd | head) to inspect it"},
		{"binary with range", `{"path":"binary.bin","offset":1,"limit":1}`, "error: binary.bin looks like a binary file (15 bytes); use bash (e.g. file, xxd | head) to inspect it"},
		{"binary at end of check window", `{"path":"later-null.bin"}`, "error: later-null.bin looks like a binary file (8008 bytes); use bash (e.g. file, xxd | head) to inspect it"},
		{"NUL after check boundary", `{"path":"after-limit.bin"}`, strings.Repeat("a", 8000) + "\x00contents"},
		{"text", `{"path":"text.txt"}`, "plain text\n"},
		{"UTF-8 text", `{"path":"utf8.txt"}`, "café 世界\n"},
		{"empty", `{"path":"empty.txt"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(tc.args); got != tc.want {
				t.Errorf("read_file = %q, want %q", got, tc.want)
			}
		})
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
		return e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "read_file", Arguments: args}}).Text
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
		return e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "edit_file", Arguments: args}}).Text
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

func TestBashSpecTimeoutOptional(t *testing.T) {
	for _, s := range Specs() {
		if s.Function.Name != "bash" {
			continue
		}
		params := s.Function.Parameters
		p := params["properties"].(map[string]any)["timeout"].(map[string]any)
		if p["type"] != "integer" {
			t.Errorf("timeout type = %v, want integer", p["type"])
		}
		for _, r := range params["required"].([]string) {
			if r == "timeout" {
				t.Error("timeout should not be required")
			}
		}
		return
	}
	t.Fatal("no bash spec")
}

func TestBashPerCallTimeout(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root, BashTimeout: 30 * time.Second}
	call := func(args string) string {
		return e.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "bash", Arguments: args}}).Text
	}

	start := time.Now()
	out := call(`{"command":"sleep 5","timeout":1}`)
	if !strings.Contains(out, "timed out after 1s") {
		t.Errorf("timeout 1: output = %q", out)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("timeout 1 took %s", d)
	}

	if out := call(`{"command":"echo ok","timeout":"2"}`); !strings.Contains(out, "<returncode>0</returncode>") || !strings.Contains(out, "ok") {
		t.Errorf("string timeout: output = %q", out)
	}

	// Omitted and 0 both fall back to the configured timeout.
	short := &Executor{Root: root, BashTimeout: 300 * time.Millisecond}
	for _, args := range []string{`{"command":"sleep 5"}`, `{"command":"sleep 5","timeout":0}`} {
		out := short.RunResult(context.Background(), model.ToolCall{Function: model.FunctionCall{Name: "bash", Arguments: args}}).Text
		if !strings.Contains(out, "timed out after 300ms") {
			t.Errorf("%s: output = %q", args, out)
		}
	}

	marker := filepath.Join(root, "ran")
	for _, tc := range []struct{ val, want string }{
		{"-1", "timeout must be between 1 and 600 seconds, got -1"},
		{"601", "timeout must be between 1 and 600 seconds, got 601"},
		{`"abc"`, "error:"},
	} {
		out := call(fmt.Sprintf(`{"command":"touch %s","timeout":%s}`, marker, tc.val))
		if !strings.Contains(out, tc.want) {
			t.Errorf("timeout %s: output = %q, want %q", tc.val, out, tc.want)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("timeout %s: command ran", tc.val)
		}
	}
}
