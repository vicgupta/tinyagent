package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

const cliSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["pass","fail"]}},"required":["verdict"],"additionalProperties":false}`

// fakeOpenAIReplies serves one chat completion per request: a submit_result
// call with each args string in turn (repeating the last), plus some prose.
func fakeOpenAIReplies(t *testing.T, requests *atomic.Int32, bodies *[]map[string]any, args ...string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(requests.Add(1)) - 1
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if bodies != nil {
			*bodies = append(*bodies, body)
		}
		a := args[min(n, len(args)-1)]
		enc, _ := json.Marshal(a)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Here is my result.","tool_calls":[{"id":"c`+string(rune('0'+n))+`","type":"function","function":{"name":"submit_result","arguments":`+string(enc)+`}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureStdout runs f with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func(f *os.File) { os.Stdout = f }(os.Stdout)
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	return <-done
}

func schemaSetup(t *testing.T, schema string) (dir, schemaPath string) {
	t.Helper()
	dir = t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	schemaPath = filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "none.yaml"), []byte("model: openai/gpt-4o\n"), 0o644)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	return dir, schemaPath
}

func TestOutputSchemaStdoutIsOnlyTheObject(t *testing.T) {
	dir, schemaPath := schemaSetup(t, cliSchema)
	var requests atomic.Int32
	var bodies []map[string]any
	srv := fakeOpenAIReplies(t, &requests, &bodies, `{ "verdict" : "pass" }`)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	traj := filepath.Join(t.TempDir(), "run.json")
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "review", "-m", "openai/gpt-4o", "--cwd", dir, "-c", filepath.Join(dir, "none.yaml"),
				"--output-schema", schemaPath, "-o", traj, "-v", "--no-stream")
		})
	})
	if code != 0 || stdout != `{"verdict":"pass"}`+"\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Here is my result.") || !strings.Contains(stderr, "Submitted after 1 steps") {
		t.Errorf("stderr = %q", stderr)
	}
	tools, _ := bodies[0]["tools"].([]any)
	last := tools[len(tools)-1].(map[string]any)["function"].(map[string]any)
	if last["name"] != "submit_result" {
		t.Errorf("last tool = %v", last)
	}
	data, err := os.ReadFile(traj)
	if err != nil {
		t.Fatal(err)
	}
	var tr trajectory.Trajectory
	if err := json.Unmarshal(data, &tr); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	json.Compact(&compact, tr.StructuredOutput)
	if compact.String() != `{"verdict":"pass"}` || tr.OutputSchemaDigest == "" || tr.Submission != `{"verdict":"pass"}` {
		t.Errorf("trajectory = %+v", tr)
	}

	// Resuming it is refused before any provider request.
	n := requests.Load()
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", traj, "--cwd", dir, "-o", filepath.Join(t.TempDir(), "next.json"))
	})
	if code != 1 || requests.Load() != n || !strings.Contains(stderr, "--output-schema cannot be resumed") {
		t.Errorf("resume: exit %d, requests %d, stderr %q", code, requests.Load()-n, stderr)
	}
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", traj, "--cwd", dir, "--output-schema", schemaPath, "-o", filepath.Join(t.TempDir(), "next.json"))
	})
	if code != 2 || requests.Load() != n || !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("resume+schema: exit %d, stderr %q", code, stderr)
	}
}

func TestOutputSchemaJSONResultEvent(t *testing.T) {
	dir, schemaPath := schemaSetup(t, cliSchema)
	var requests atomic.Int32
	srv := fakeOpenAIReplies(t, &requests, nil, `{"verdict":"fail"}`)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	var code int
	var stdout string
	captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "-c", filepath.Join(dir, "none.yaml"), "--output-schema", schemaPath, "--json")
		})
	})
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	var first, last map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			t.Fatalf("line %d not JSON: %q", i, line)
		}
	}
	if code != 0 || first["schema_version"] != float64(1) || last["type"] != "result" || last["status"] != agent.StatusSubmitted ||
		last["submission"] != `{"verdict":"fail"}` || last["structured_output"].(map[string]any)["verdict"] != "fail" {
		t.Errorf("exit %d, first %v, result %v", code, first, last)
	}
}

func TestOutputSchemaInvalidOutputExit1(t *testing.T) {
	dir, schemaPath := schemaSetup(t, cliSchema)
	var requests atomic.Int32
	srv := fakeOpenAIReplies(t, &requests, nil, `{"verdict":"maybe"}`)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	var code int
	var stdout, stderr string
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", filepath.Join(dir, "none.yaml"), "--output-schema", schemaPath)
		})
	})
	if code != 1 || stdout != "" || !strings.Contains(stderr, agent.StatusInvalidOutput) || int(requests.Load()) != agent.MaxInvalidOutputs {
		t.Errorf("exit %d, stdout %q, requests %d, stderr %q", code, stdout, requests.Load(), stderr)
	}
	// JSONL: the result event has no structured_output.
	stdout = captureStdout(t, func() {
		captureStderr(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "-c", filepath.Join(dir, "none.yaml"), "--output-schema", schemaPath, "--json")
		})
	})
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	var last map[string]any
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if _, has := last["structured_output"]; code != 1 || has || last["status"] != agent.StatusInvalidOutput {
		t.Errorf("json: exit %d, result %v", code, last)
	}
}

func TestBadOutputSchemaExits2BeforeProvider(t *testing.T) {
	for name, schema := range map[string]string{
		"invalid JSON": `{`,
		"non-object":   `{"type":"string"}`,
		"keyword":      `{"type":"object","anyOf":[]}`,
	} {
		dir, schemaPath := schemaSetup(t, schema)
		var requests atomic.Int32
		srv := fakeOpenAI(t, &requests)
		t.Setenv("OPENAI_BASE_URL", srv.URL)
		var code int
		stderr := captureStderr(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "--output-schema", schemaPath)
		})
		if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, schemaPath) {
			t.Errorf("%s: exit %d, requests %d, stderr %q", name, code, requests.Load(), stderr)
		}
	}
	dir, _ := schemaSetup(t, "{}")
	missing := filepath.Join(dir, "missing.json")
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "--output-schema", missing)
	})
	if code != 2 || !strings.Contains(stderr, missing) || !strings.Contains(stderr, "does not exist") {
		t.Errorf("missing: exit %d, stderr %q", code, stderr)
	}
}
