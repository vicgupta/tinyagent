package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
)

// TestActionableErrorWording asserts that key tool errors state a cause and a
// concrete next step (issue #378), matching edit_file's unique-match style.
func TestActionableErrorWording(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "x.go", "foo := 1\nfoo := 2\nfoo := 3\n"); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, BashTimeout: time.Second}

	cases := []struct {
		name, tool, args string
		want             []string
	}{
		{
			"edit unique", "edit_file",
			`{"path":"x.go","old_str":"foo","new_str":"n"}`,
			[]string{"occurs 3 times", "add surrounding context to make it unique"},
		},
		{
			"edit missing", "edit_file",
			`{"path":"x.go","old_str":"nope","new_str":"n"}`,
			[]string{"old_str not found", "read the file and copy the exact text"},
		},
		{
			"edit empty", "edit_file",
			`{"path":"x.go","old_str":"","new_str":"n"}`,
			[]string{"old_str must not be empty", "pass the exact text to replace"},
		},
		{
			"read past end", "read_file",
			`{"path":"x.go","offset":99}`,
			[]string{"past the end of the file", "use a smaller offset"},
		},
		{
			"path outside", "read_file",
			`{"path":".."}`,
			[]string{"outside the working directory", "use a path under the working directory"},
		},
		{
			"grep bad regexp", "grep",
			`{"pattern":"("}`,
			[]string{"bad regexp", "fixed_strings"},
		},
		{
			"bash bad timeout", "bash",
			`{"command":"true","timeout":9999}`,
			[]string{"timeout must be between", "pass timeout from 1"},
		},
		{
			"unknown tool", "no_such_tool",
			`{}`,
			[]string{"unknown tool", "call one of the tools listed"},
		},
		{
			"undo missing path", UndoToolName,
			`{}`,
			[]string{"path is required", "write_file, edit_file or apply_patch"},
		},
		{
			"start_process empty", "start_process",
			`{"command":"  "}`,
			[]string{"command is required", "non-empty bash command"},
		},
		{
			"read_process missing", "read_process",
			`{"id":"p999"}`,
			[]string{"no background process", "start_process"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := e.RunResult(context.Background(), model.ToolCall{
				Function: model.FunctionCall{Name: c.tool, Arguments: c.args},
			})
			if !r.IsError {
				t.Fatalf("%s = %q, want an error", c.tool, r.Text)
			}
			for _, w := range c.want {
				if !strings.Contains(r.Text, w) {
					t.Errorf("%s = %q, want substring %q", c.tool, r.Text, w)
				}
			}
			if !strings.Contains(r.Text, ";") {
				t.Errorf("%s = %q, want a cause; next-step shape", c.tool, r.Text)
			}
		})
	}
}

func TestActionableWebFetchErrors(t *testing.T) {
	f, err := NewWebFetcher([]string{"example.com"}, 1024, time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, url string
		want      []string
	}{
		{"empty", "", []string{"url is required", "pass an http"}},
		{"scheme", "ftp://example.com/", []string{"only http and https", "http:// or https://"}},
		{"host allowlist", "https://evil.example/", []string{"not in web_fetch.allowed_domains", "pick an allowed host"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.Fetch(context.Background(), c.url)
			if err == nil {
				t.Fatal("want error")
			}
			got := err.Error()
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("got %q, want substring %q", got, w)
				}
			}
		})
	}
	f2, err := NewWebFetcher(nil, 1024, 50*time.Millisecond, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	err = f2.ctxError(ctx, context.DeadlineExceeded)
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "retry later") {
		t.Errorf("timeout = %v", err)
	}
}

func TestActionableWebSearchErrors(t *testing.T) {
	s, err := NewWebSearcher(WebSearchOptions{Providers: []string{"duckduckgo"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Search(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "query is required") || !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("empty query = %v", err)
	}
	long := strings.Repeat("a", maxWebSearchQueryLen+1)
	_, err = s.Search(context.Background(), long)
	if err == nil || !strings.Contains(err.Error(), "longer than") || !strings.Contains(err.Error(), "shorten") {
		t.Errorf("long query = %v", err)
	}
}

func TestActionableHelper(t *testing.T) {
	err := Actionable("cause here", "try this next")
	if got := err.Error(); got != "cause here; try this next" {
		t.Errorf("Actionable = %q", got)
	}
	err = Actionablef("do X", "y failed: %d", 3)
	if got := err.Error(); got != "y failed: 3; do X" {
		t.Errorf("Actionablef = %q", got)
	}
}

func TestActionableDeniedError(t *testing.T) {
	err := &DeniedError{Path: ".env", List: "deny_read", Pattern: ".env"}
	got := err.Error()
	if !strings.Contains(got, `denied by file_access.deny_read rule ".env"`) || !strings.Contains(got, "choose a different path") {
		t.Errorf("DeniedError = %q", got)
	}
}

func TestActionableBashTimeoutMessage(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root, BashTimeout: 50 * time.Millisecond}
	r := e.RunResult(context.Background(), model.ToolCall{
		Function: model.FunctionCall{Name: "bash", Arguments: `{"command":"sleep 5"}`},
	})
	if !strings.Contains(r.Text, "timed out") || !strings.Contains(r.Text, "retry with a higher timeout") {
		t.Errorf("bash timeout = %q", r.Text)
	}
}

func TestActionableReadOnlyTool(t *testing.T) {
	e := &Executor{Root: t.TempDir(), ReadOnly: true}
	r := e.RunResult(context.Background(), model.ToolCall{
		Function: model.FunctionCall{Name: "bash", Arguments: `{"command":"true"}`},
	})
	if !r.IsError || !strings.Contains(r.Text, "read-only mode") || !strings.Contains(r.Text, "use read_file") {
		t.Errorf("read-only = %q", r.Text)
	}
}
