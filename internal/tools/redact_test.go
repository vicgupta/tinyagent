package tools

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/skills"
)

const testSecret = "s3cr3tvalue123"

func redactor() *eventlog.Redactor { return eventlog.NewValueRedactor([]string{testSecret}, false) }

// TestRedactsCoveredTools guards the list of tools redact_secrets covers:
// a new built-in that returns file contents must be added to
// unredactedTools, and every other tool is masked.
func TestRedactsCoveredTools(t *testing.T) {
	for _, n := range []string{"bash", "start_process", "read_process", "stop_process", WebFetchToolName, "github__get_issue"} {
		if !Redacts(n) {
			t.Errorf("Redacts(%q) = false", n)
		}
	}
	for _, n := range []string{"read_file", "write_file", "edit_file", "list_files", "grep", ApplyPatchToolName, ViewImageToolName, LSPToolName, skills.ToolName} {
		if Redacts(n) {
			t.Errorf("Redacts(%q) = true", n)
		}
	}
}

func TestRedactMCPResultBeforeTruncation(t *testing.T) {
	out := strings.Repeat("a", 45) + testSecret + strings.Repeat("b", 45)
	e := &Executor{Root: t.TempDir(), MaxOutputChars: 80, Redactor: redactor(),
		ExternalCall: func(_ context.Context, name, _ string) (string, bool, bool) { return out, false, name == "srv__echo" }}
	r := e.RunResult(context.Background(), toolCall("1", "srv__echo", nil))
	if r.Redactions != 1 {
		t.Errorf("Redactions = %d", r.Redactions)
	}
	for _, part := range []string{testSecret, testSecret[:7], testSecret[7:]} {
		if strings.Contains(r.Text, part) {
			t.Errorf("result contains %q: %q", part, r.Text)
		}
	}
}

func TestRedactBeforeOutputFile(t *testing.T) {
	e := outputExecutor(t, 200)
	e.Redactor = redactor()
	out := strings.Repeat("x\n", 300) + testSecret + "\n"
	e.ExternalCall = func(_ context.Context, _, _ string) (string, bool, bool) { return out, false, true }
	r := e.RunResult(context.Background(), toolCall("c1", "srv__dump", nil))
	if r.OutputFile == "" {
		t.Fatalf("no output file: %q", r.Text)
	}
	data, err := os.ReadFile(filepath.Join(e.Root, filepath.FromSlash(r.OutputFile)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testSecret) || !strings.Contains(string(data), eventlog.Redacted) {
		t.Errorf("saved file not redacted: %q", data[len(data)-40:])
	}
}

func TestRedactSkipsFileTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("key "+testSecret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, Redactor: redactor()}
	for _, c := range []struct {
		name string
		args map[string]any
	}{{"read_file", map[string]any{"path": "f.txt"}}, {"grep", map[string]any{"pattern": "key"}}} {
		if r := e.RunResult(context.Background(), toolCall("1", c.name, c.args)); !strings.Contains(r.Text, testSecret) || r.Redactions != 0 {
			t.Errorf("%s result redacted: %q", c.name, r.Text)
		}
	}
}

func TestRedactWebFetch(t *testing.T) {
	srv, f := wfServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("token=" + testSecret))
	})
	e := &Executor{Root: t.TempDir(), WebFetch: f, Redactor: redactor()}
	r := e.RunResult(context.Background(), toolCall("1", WebFetchToolName, map[string]any{"url": srv.URL + "/x"}))
	if strings.Contains(r.Text, testSecret) || !strings.Contains(r.Text, "token="+eventlog.Redacted) || r.Redactions != 1 {
		t.Errorf("web_fetch result = %q (%d redactions)", r.Text, r.Redactions)
	}
}

func TestRedactOffByDefault(t *testing.T) {
	e := &Executor{Root: t.TempDir(),
		ExternalCall: func(_ context.Context, _, _ string) (string, bool, bool) { return testSecret, false, true }}
	if r := e.RunResult(context.Background(), toolCall("1", "srv__echo", nil)); r.Text != testSecret || r.Redactions != 0 {
		t.Errorf("result = %q", r.Text)
	}
}
