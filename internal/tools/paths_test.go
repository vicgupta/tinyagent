package tools

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

func TestResultPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "a", "x.go"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, ApplyPatch: true}
	abs := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
	run := func(name, args string) Result {
		return e.RunResult(context.Background(), model.ToolCall{ID: "1", Type: "function", Function: model.FunctionCall{Name: name, Arguments: args}})
	}
	for _, c := range []struct {
		name, args string
		want       []string
	}{
		{"read_file", `{"path":"pkg/a/x.go"}`, []string{abs("pkg/a/x.go")}},
		{"list_files", `{}`, []string{root}},
		{"list_files", `{"path":"pkg/a"}`, []string{abs("pkg/a")}},
		{"grep", `{"pattern":"old","path":"pkg"}`, []string{abs("pkg")}},
		{"write_file", `{"path":"pkg/b/y.go","content":"y"}`, []string{abs("pkg/b/y.go")}},
		{"edit_file", `{"path":"pkg/a/x.go","old_str":"old","new_str":"mid"}`, []string{abs("pkg/a/x.go")}},
		{ApplyPatchToolName, `{"input":"*** Begin Patch\n*** Update File: pkg/a/x.go\n*** Move to: pkg/c/x.go\n@@\n-mid\n+new\n*** Add File: pkg/d/z.go\n+z\n*** End Patch"}`,
			[]string{abs("pkg/a/x.go"), abs("pkg/c/x.go"), abs("pkg/d/z.go")}},
	} {
		r := run(c.name, c.args)
		if r.IsError {
			t.Errorf("%s %s: %s", c.name, c.args, r.Text)
			continue
		}
		got := slices.Clone(r.Paths)
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s %s: paths %v, want %v", c.name, c.args, r.Paths, c.want)
		}
	}
	for _, c := range [][2]string{
		{"read_file", `{"path":"missing"}`},
		{"read_file", `{"path":"../x"}`},
		{"bash", `{"command":"true"}`},
	} {
		if r := run(c[0], c[1]); len(r.Paths) != 0 {
			t.Errorf("%s %s: paths %v, want none", c[0], c[1], r.Paths)
		}
	}
}
