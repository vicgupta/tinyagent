package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/ratchet"
)

func ratchetEnv(t *testing.T) string {
	t.Helper()
	cwd, _ := memoryEnv(t)
	old := ratchetNow
	ratchetNow = func() time.Time { return time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { ratchetNow = old })
	return cwd
}

func runRatchetCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			var flagErr string
			code, _, flagErr = runWith(t, args...)
			os.Stderr.WriteString(flagErr)
		})
	})
	return
}

func TestRatchetAddListRemove(t *testing.T) {
	cwd := ratchetEnv(t)
	code, out, errOut := runRatchetCLI(t, "ratchet", "add", "Never push to main", "--incident", "pushed a broken commit to main", "--deny-command", "git push * main", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "added rule r1") || !strings.Contains(out, "expires 2027-01-28") {
		t.Fatalf("add: exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	code, out, errOut = runRatchetCLI(t, "--ratchet-add", "Run go vet", "--ratchet-incident", "vet failed in CI", "--ratchet-review", "when CI runs vet on every push", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "added rule r2") || !strings.Contains(out, "review: when CI") {
		t.Fatalf("add r2: exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	code, out, _ = runRatchetCLI(t, "ratchet", "list", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "r1  active  2026-10-30  Never push to main [enforced]") || !strings.Contains(out, "incident: vet failed in CI") {
		t.Fatalf("list: exit %d\n%s", code, out)
	}
	code, out, _ = runRatchetCLI(t, "ratchet", "list", "--json", "--cwd", cwd)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); code != 0 || err != nil || len(rows) != 2 || rows[0]["enforced"] != true || rows[1]["review"] == nil {
		t.Fatalf("list --json: exit %d err %v\n%s", code, err, out)
	}
	code, out, _ = runRatchetCLI(t, "ratchet", "path", "--cwd", cwd)
	if code != 0 || !strings.HasSuffix(strings.TrimSpace(out), filepath.FromSlash(ratchet.RelPath)) {
		t.Fatalf("path: %d %q", code, out)
	}
	code, out, _ = runRatchetCLI(t, "ratchet", "remove", "r1", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "removed rule r1") {
		t.Fatalf("remove: %d %q", code, out)
	}
	rules, err := ratchet.Load(ratchet.Path(testRoot(t, cwd)))
	if err != nil || len(rules) != 1 || rules[0].ID != "r2" {
		t.Fatalf("after remove: %+v %v", rules, err)
	}
}

func testRoot(t *testing.T, cwd string) string {
	t.Helper()
	root, err := canonicalRoot(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRatchetUsageErrors(t *testing.T) {
	cwd := ratchetEnv(t)
	for _, args := range [][]string{
		{"--ratchet-add", "x"},
		{"--ratchet-incident", "x"},
		{"--ratchet-add", "x", "--ratchet-incident", "y", "--ratchet-expires", "tomorrow"},
		{"--ratchet-add", "x", "--ratchet-incident", "y", "--ratchet-deny-command", "a", "--ratchet-deny-path", "b"},
		{"--ratchet-add", "x", "--ratchet-incident", "y", "--ratchet-deny-host", "bad host!"},
		{"--ratchet-list", "--ratchet-path"},
		{"--ratchet-list", "-t", "task"},
		{"--ratchet-path", "--json"},
		{"--ratchet-remove", " "},
	} {
		code, _, _ := runRatchetCLI(t, append(args, "--cwd", cwd)...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if _, err := os.Stat(ratchet.Path(testRoot(t, cwd))); !os.IsNotExist(err) {
		t.Fatalf("usage errors created the rules file: %v", err)
	}
	if code, _, _ := runRatchetCLI(t, "--ratchet-remove", "r9", "--cwd", cwd); code != 1 {
		t.Errorf("remove unknown: exit %d, want 1", code)
	}
}

// ratchetModel is a fake model that first runs a bash command and then
// finishes, recording every request body.
func ratchetModel(t *testing.T, command string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 && command != "" {
			args, _ := json.Marshal(map[string]string{"command": command})
			resp, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}},
				"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
			})
			w.Write(resp)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func seedRatchet(t *testing.T, cwd, body string) {
	t.Helper()
	p := ratchet.Path(testRoot(t, cwd))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const ratchetSeed = `
[[rule]]
id = "r1"
rule = "Don't delete the keep directory"
incident = "an agent wiped keep/ during cleanup"
created = "2026-10-01"
review = "when keep/ is generated"
[rule.deny]
command = "rm *"

[[rule]]
id = "r2"
rule = "stale-rule-text"
incident = "old"
created = "2026-01-01"
expires = "2026-06-01"
`

func TestRatchetRunInjectsAndEnforces(t *testing.T) {
	cwd := ratchetEnv(t)
	bodies := ratchetModel(t, "rm -rf keep")
	seedRatchet(t, cwd, ratchetSeed)
	keep := filepath.Join(cwd, "keep", "f")
	os.MkdirAll(filepath.Dir(keep), 0o755)
	os.WriteFile(keep, []byte("x"), 0o644)
	code, stdout, stderr := runMemoryTaskOut(t, cwd, "--json")
	if code != 0 {
		t.Fatalf("exit %d\nstderr %s", code, stderr)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("denied command ran: %v", err)
	}
	b := bodies()
	if len(b) < 2 {
		t.Fatalf("want 2 requests, got %d", len(b))
	}
	if !strings.Contains(b[0], "Project rules") || !strings.Contains(b[0], "[r1] Don't delete the keep directory") || strings.Contains(b[0], "stale-rule-text") {
		t.Fatalf("system prompt missing rules or has expired rule:\n%s", b[0])
	}
	if !strings.Contains(b[1], "blocked by project rule r1") {
		t.Fatalf("deny message not shown to model:\n%s", b[1])
	}
	if !strings.Contains(stdout, `"ratchet.loaded"`) || !strings.Contains(stdout, `"expired":["r2"]`) || !strings.Contains(stdout, "project rule r2 expired") {
		t.Fatalf("events:\n%s", stdout)
	}
}

func TestRatchetRunOffAndAbsent(t *testing.T) {
	cwd := ratchetEnv(t)
	bodies := ratchetModel(t, "")
	code, stdout, stderr := runMemoryTaskOut(t, cwd, "--json")
	if code != 0 || strings.Contains(stdout, "ratchet.loaded") || strings.Contains(bodies()[0], "Project rules") {
		t.Fatalf("no rules file: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".tinyagent")); !os.IsNotExist(err) {
		t.Fatalf("a run without rules created .tinyagent: %v", err)
	}
	seedRatchet(t, cwd, ratchetSeed)
	code, stdout, _ = runMemoryTaskOut(t, cwd, "--json", "--no-ratchet")
	if code != 0 || strings.Contains(stdout, "ratchet.loaded") || strings.Contains(bodies()[1], "Project rules") {
		t.Fatalf("--no-ratchet: exit %d\n%s", code, stdout)
	}
	seedRatchet(t, cwd, "[[rule]\n")
	if code, _, stderr := runMemoryTaskOut(t, cwd); code != 2 || !strings.Contains(stderr, "--no-ratchet") {
		t.Fatalf("corrupt file: exit %d\n%s", code, stderr)
	}
}
