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
)

const responsesSubmit = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`

// fakeResponses serves Responses API replies in turn (repeating the last),
// recording each request path and body.
type fakeResponses struct {
	mu      sync.Mutex
	paths   []string
	bodies  []map[string]any
	replies []string
}

func (f *fakeResponses) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.bodies = append(f.bodies, b)
		reply := f.replies[min(len(f.bodies)-1, len(f.replies)-1)]
		f.mu.Unlock()
		if strings.HasPrefix(reply, "400 ") {
			w.WriteHeader(http.StatusBadRequest)
			reply = strings.TrimPrefix(reply, "400 ")
		}
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func openaiAPISetup(t *testing.T) (dir, emptyCfg string) {
	t.Helper()
	dir = t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	emptyCfg = filepath.Join(dir, "none.yaml")
	os.WriteFile(emptyCfg, nil, 0o644)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	return dir, emptyCfg
}

func runStarted(t *testing.T, stdout string) map[string]any {
	t.Helper()
	first, _, _ := strings.Cut(stdout, "\n")
	var ev map[string]any
	if err := json.Unmarshal([]byte(first), &ev); err != nil || ev["event"] != "run.started" && ev["type"] != "run.started" && ev["kind"] != "run.started" {
		t.Fatalf("first event %q: %v", first, err)
	}
	return ev
}

// TestOpenAIAPIRunStarted covers the flag and config choosing the transport
// and run.started recording it (AC6, AC13).
func TestOpenAIAPIRunStarted(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	chatCfg := filepath.Join(dir, "chat.yaml")
	os.WriteFile(chatCfg, []byte("openai_api: chat\n"), 0o644)
	for _, c := range []struct {
		name, path, want string
		args             []string
	}{
		{"flag responses", "/responses", "responses", []string{"-c", emptyCfg, "--openai-api", "responses"}},
		{"config chat", "/chat/completions", "chat", []string{"-c", chatCfg}},
		{"flag beats config", "/responses", "responses", []string{"-c", chatCfg, "--openai-api", "responses"}},
		{"auto with a custom base", "/chat/completions", "chat", []string{"-c", emptyCfg}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResponses{replies: []string{responsesSubmit}}
			if c.want == "chat" {
				f.replies = []string{`{"choices":[{"message":{"role":"assistant","content":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`}
			}
			t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
			var code int
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() {
					code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--json"}, c.args...)...)
				})
			})
			if code != 0 || len(f.paths) != 1 || f.paths[0] != c.path {
				t.Fatalf("exit %d, paths %v; stderr:\n%s", code, f.paths, stderr)
			}
			if got := runStarted(t, stdout)["openai_api"]; got != c.want {
				t.Errorf("run.started openai_api = %v, want %s", got, c.want)
			}
		})
	}
}

func TestOpenAIAPIVerboseAndOtherProviders(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	f := &fakeResponses{replies: []string{responsesSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "-c", emptyCfg, "--openai-api", "responses", "--no-stream", "-v")
	})
	if code != 0 || !strings.Contains(stderr, "openai transport: responses") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}

	// Other providers ignore the setting and record nothing.
	chat := fakeOpenAIBodies(t, new([]map[string]any))
	t.Setenv("OLLAMA_HOST", chat.URL)
	var stdout string
	captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "ollama/llama3.1", "--cwd", dir, "-c", emptyCfg, "--openai-api", "responses", "--json")
		})
	})
	if code != 0 {
		t.Fatalf("ollama exit %d", code)
	}
	if _, ok := runStarted(t, stdout)["openai_api"]; ok {
		t.Error("ollama run.started has openai_api")
	}
}

func TestBadOpenAIAPIExits2(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	badCfg := filepath.Join(dir, "bad.yaml")
	os.WriteFile(badCfg, []byte("openai_api: completions\n"), 0o644)
	f := &fakeResponses{replies: []string{responsesSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	for _, args := range [][]string{
		{"-c", emptyCfg, "--openai-api", "bogus"},
		{"-c", badCfg},
	} {
		var code int
		stderr := captureStderr(t, func() {
			code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--no-stream"}, args...)...)
		})
		if code != 2 || len(f.paths) != 0 || !strings.Contains(stderr, "auto, responses, chat") {
			t.Errorf("%v: exit %d, %d requests, stderr %q", args, code, len(f.paths), stderr)
		}
	}
}

// TestOutputSchemaOnResponses covers --output-schema on the Responses
// transport (AC11).
func TestOutputSchemaOnResponses(t *testing.T) {
	dir, schemaPath := schemaSetup(t, cliSchema)
	f := &fakeResponses{replies: []string{`{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"submit_result","arguments":"{\"verdict\":\"pass\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "review", "-m", "openai/gpt-5", "--cwd", dir, "-c", filepath.Join(dir, "none.yaml"),
				"--openai-api", "responses", "--output-schema", schemaPath, "--no-stream")
		})
	})
	if code != 0 || stdout != `{"verdict":"pass"}`+"\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	tools, _ := f.bodies[0]["tools"].([]any)
	if last := tools[len(tools)-1].(map[string]any); last["name"] != "submit_result" || last["type"] != "function" {
		t.Errorf("last tool = %v", last)
	}
}

// TestChatResponsesHint covers the hint on a Chat Completions 400 that asks
// for the Responses API (AC12).
func TestChatResponsesHint(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	f := &fakeResponses{replies: []string{`400 {"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6 in /v1/chat/completions. Please use /v1/responses instead.","type":"invalid_request_error"}}`}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-6", "--cwd", dir, "-c", emptyCfg, "--no-stream", "--reasoning-effort", "high")
	})
	if code == 0 || !strings.Contains(stderr, "openai_api: responses") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// TestResumeKeepsOpenAIAPI covers resume using the recorded transport and
// replaying reasoning items (AC14).
func TestResumeKeepsOpenAIAPI(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	f := &fakeResponses{replies: []string{
		`{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC1"},{"type":"function_call","call_id":"c1","name":"read_file","arguments":"{\"path\":\"a.txt\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		responsesSubmit,
	}}
	// A custom base URL: auto would pick chat, so only the recorded value
	// keeps the resumed run on Responses.
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	traj := filepath.Join(t.TempDir(), "run.json")
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "-c", emptyCfg, "--openai-api", "responses", "--no-stream", "--step-limit", "1", "-o", traj)
	})
	data, err := os.ReadFile(traj)
	if err != nil {
		t.Fatalf("exit %d, no trajectory: %v; stderr:\n%s", code, err, stderr)
	}
	if !strings.Contains(string(data), `"openai_api": "responses"`) {
		t.Fatalf("trajectory does not record the transport:\n%s", data)
	}

	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", traj, "--cwd", dir, "-c", emptyCfg, "--openai-api", "chat", "--no-stream", "-o", filepath.Join(t.TempDir(), "x.json"))
	})
	if code == 0 || len(f.paths) != 1 || !strings.Contains(stderr, "conflicts with the checkpoint's openai_api") {
		t.Fatalf("conflicting resume: exit %d, %d requests, stderr %q", code, len(f.paths), stderr)
	}

	next := filepath.Join(t.TempDir(), "next.json")
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", traj, "--cwd", dir, "-c", emptyCfg, "--no-stream", "-o", next)
	})
	if code != 0 || len(f.paths) != 2 || f.paths[1] != "/responses" {
		t.Fatalf("resume: exit %d, paths %v; stderr:\n%s", code, f.paths, stderr)
	}
	input, _ := f.bodies[1]["input"].([]any)
	var replayed bool
	for i, it := range input {
		m := it.(map[string]any)
		if m["type"] == "reasoning" {
			replayed = m["encrypted_content"] == "ENC1" && i+1 < len(input) && input[i+1].(map[string]any)["type"] == "function_call"
		}
	}
	if !replayed {
		t.Errorf("reasoning not replayed before the function_call: %v", input)
	}
	if data, _ := os.ReadFile(next); !strings.Contains(string(data), `"openai_api": "responses"`) {
		t.Errorf("resumed trajectory does not record the transport")
	}
}
