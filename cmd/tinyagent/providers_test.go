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

	"github.com/vicgupta/tinyagent/internal/model"
)

const chatSubmit = `{"choices":[{"message":{"role":"assistant","content":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`

// fakeGateway serves replies in turn (repeating the last), recording each
// request's path, headers and body.
type fakeGateway struct {
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []map[string]any
	replies []string
}

func (f *fakeGateway) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.headers = append(f.headers, r.Header.Clone())
		f.bodies = append(f.bodies, b)
		reply := f.replies[min(len(f.bodies)-1, len(f.replies)-1)]
		f.mu.Unlock()
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeGateway) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func providerCfg(t *testing.T, dir, body string) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "providers-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

// TestProviderChatRun covers AC1 and AC13 plus the -v line.
func TestProviderChatRun(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	f := &fakeGateway{replies: []string{chatSubmit}}
	srv := f.start(t)
	cfg := providerCfg(t, dir, "providers:\n  - id: local\n    api: chat\n    base_url: "+srv.URL+"\n")
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "local/m1", "--cwd", dir, "-c", cfg, "--json", "-v")
	if code != 0 || f.count() != 1 {
		t.Fatalf("exit %d, %d requests; stderr:\n%s", code, f.count(), stderr)
	}
	if f.paths[0] != "/chat/completions" || f.bodies[0]["model"] != "m1" {
		t.Errorf("path %s, model %v", f.paths[0], f.bodies[0]["model"])
	}
	if _, ok := f.headers[0]["Authorization"]; ok {
		t.Error("Authorization sent with no api_key_env")
	}
	ev := runStarted(t, stdout)
	if ev["provider_api"] != "chat" || ev["provider_base_url"] != srv.URL || ev["model"] != "local/m1" {
		t.Errorf("run.started = %v", ev)
	}
	if !strings.Contains(stderr, "provider: local (api chat, base_url "+srv.URL+")") {
		t.Errorf("no -v provider line:\n%s", stderr)
	}
	if strings.Contains(stderr, "plain http") {
		t.Errorf("warned for a loopback http base_url:\n%s", stderr)
	}
}

// TestProviderAPIs covers AC2 for responses and anthropic through the CLI.
func TestProviderAPIs(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	for _, c := range []struct{ api, path, reply string }{
		{"responses", "/responses", responsesSubmit},
		{"anthropic", "/v1/messages", `{"content":[{"type":"text","text":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`},
	} {
		f := &fakeGateway{replies: []string{c.reply}}
		srv := f.start(t)
		cfg := providerCfg(t, dir, "providers:\n  - id: gw\n    api: "+c.api+"\n    base_url: "+srv.URL+"\n")
		code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/vendor/m", "--cwd", dir, "-c", cfg, "--no-stream")
		if code != 0 || f.count() != 1 || f.paths[0] != c.path || f.bodies[0]["model"] != "vendor/m" {
			t.Errorf("%s: exit %d, paths %v, bodies %v; stderr:\n%s", c.api, code, f.paths, f.bodies, stderr)
		}
	}
}

// TestProviderMissingEnvExits2 covers AC3 and AC4's failure cases.
func TestProviderMissingEnvExits2(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	f := &fakeGateway{replies: []string{chatSubmit}}
	srv := f.start(t)
	cfg := providerCfg(t, dir, "providers:\n  - id: gw\n    api: chat\n    base_url: "+srv.URL+"\n    api_key_env: GATEWAY_API_KEY\n    env_headers: {X-Tenant: GATEWAY_TENANT}\n")
	t.Setenv("GATEWAY_TENANT", "t1")
	for _, key := range []string{"", "unset"} {
		if key == "unset" {
			os.Unsetenv("GATEWAY_API_KEY")
		} else {
			t.Setenv("GATEWAY_API_KEY", key)
		}
		code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/m", "--cwd", dir, "-c", cfg, "--no-stream")
		if code != 2 || f.count() != 0 || !strings.Contains(stderr, "GATEWAY_API_KEY") || !strings.Contains(stderr, `"gw"`) {
			t.Errorf("key %q: exit %d, %d requests, stderr %q", key, code, f.count(), stderr)
		}
	}
	t.Setenv("GATEWAY_API_KEY", "k")
	t.Setenv("GATEWAY_TENANT", "")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/m", "--cwd", dir, "-c", cfg, "--no-stream")
	if code != 2 || f.count() != 0 || !strings.Contains(stderr, "GATEWAY_TENANT") {
		t.Errorf("env header: exit %d, %d requests, stderr %q", code, f.count(), stderr)
	}
	// Set: the key and headers ride every request.
	t.Setenv("GATEWAY_TENANT", "t1")
	cfg = providerCfg(t, dir, "providers:\n  - id: gw\n    api: chat\n    base_url: "+srv.URL+"\n    api_key_env: GATEWAY_API_KEY\n    headers: {X-Team: core}\n    env_headers: {X-Tenant: GATEWAY_TENANT}\n")
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/m", "--cwd", dir, "-c", cfg, "--no-stream"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	h := f.headers[0]
	if h.Get("Authorization") != "Bearer k" || h.Get("X-Team") != "core" || h.Get("X-Tenant") != "t1" {
		t.Errorf("headers %v", h)
	}
}

// TestProviderSecretsNeverRecorded covers AC5: key and header values stay
// out of stderr, -v, --json, the trajectory and --log-db at both levels,
// and the log-db redacts them even when they show up in tool output.
func TestProviderSecretsNeverRecorded(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	const key, tenant = "sk-gw-SECRETKEYVALUE", "tenant-SECRETHEADER"
	t.Setenv("GW_CRED", key)
	t.Setenv("GW_ORG", tenant)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(key+" "+tenant), 0o644)
	readNotes := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"notes.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	for _, level := range []string{"metadata", "full"} {
		t.Run(level, func(t *testing.T) {
			f := &fakeGateway{replies: []string{chatSubmit}}
			srv := f.start(t)
			db := filepath.Join(t.TempDir(), "log.db")
			cfg := providerCfg(t, dir, "providers:\n  - id: gw\n    api: chat\n    base_url: "+srv.URL+"\n    api_key_env: GW_CRED\n    env_headers: {X-Org: GW_ORG}\n"+
				"log_db:\n  enabled: true\n  path: "+db+"\n  level: "+level+"\n")
			traj := filepath.Join(t.TempDir(), "run.json")
			code, stdout, stderr := runCLI(t, "-t", "x", "-m", "gw/m", "--cwd", dir, "-c", cfg, "--json", "-v", "-o", traj)
			if code != 0 || f.headers[0].Get("Authorization") != "Bearer "+key {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			data, _ := os.ReadFile(traj)
			dbData := readDB(t, db)
			for name, s := range map[string]string{"stdout": stdout, "stderr": stderr, "trajectory": string(data), "log-db": dbData} {
				if strings.Contains(s, key) || strings.Contains(s, tenant) {
					t.Errorf("%s contains a provider secret", name)
				}
			}
			if !strings.Contains(string(data), `"provider_api": "chat"`) {
				t.Errorf("trajectory lacks provider_api")
			}

			// Tool output carrying the values is redacted in the log-db.
			f2 := &fakeGateway{replies: []string{readNotes, chatSubmit}}
			srv2 := f2.start(t)
			db2 := filepath.Join(t.TempDir(), "log2.db")
			cfg = providerCfg(t, dir, "providers:\n  - id: gw\n    api: chat\n    base_url: "+srv2.URL+"\n    api_key_env: GW_CRED\n    env_headers: {X-Org: GW_ORG}\n"+
				"log_db:\n  enabled: true\n  path: "+db2+"\n  level: "+level+"\n")
			if code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/m", "--cwd", dir, "-c", cfg, "--no-stream"); code != 0 {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			if s := readDB(t, db2); strings.Contains(s, key) || strings.Contains(s, tenant) {
				t.Errorf("log-db did not redact provider secrets in tool output")
			}
		})
	}
}

// readDB returns the bytes of a SQLite database and its WAL files.
func readDB(t *testing.T, path string) string {
	t.Helper()
	var b strings.Builder
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, _ := os.ReadFile(path + suffix)
		b.Write(data)
	}
	if b.Len() == 0 {
		t.Fatalf("no log-db at %s", path)
	}
	return b.String()
}

// TestProviderConfigErrorsExit2 covers AC6 through the CLI.
func TestProviderConfigErrorsExit2(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	f := &fakeGateway{replies: []string{chatSubmit}}
	srv := f.start(t)
	for _, entry := range []string{
		"  - id: openai\n    api: chat\n    base_url: " + srv.URL + "\n",
		"  - id: a/b\n    api: chat\n    base_url: " + srv.URL + "\n",
		"  - id: gw\n    api: chat\n    base_url: " + srv.URL + "\n  - id: gw\n    api: chat\n    base_url: " + srv.URL + "\n",
		"  - id: gw\n    api: grpc\n    base_url: " + srv.URL + "\n",
		"  - id: gw\n    api: chat\n    base_url: ftp://x\n",
		"  - id: gw\n    api: chat\n    base_url: relative/path\n",
		"  - id: gw\n    api: chat\n    base_url: " + srv.URL + "\n    models:\n      m: {output_price_per_mtok: -2}\n",
	} {
		cfg := providerCfg(t, dir, "providers:\n"+entry)
		code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/m", "--cwd", dir, "-c", cfg, "--no-stream")
		if code != 2 || f.count() != 0 || !strings.Contains(stderr, cfg) || !strings.Contains(stderr, "providers[") {
			t.Errorf("%q: exit %d, %d requests, stderr %q", entry, code, f.count(), stderr)
		}
	}
	cfg := providerCfg(t, dir, "providers:\n  - id: gw\n    api: chat\n    base_url: "+srv.URL+"\n    base_ur: typo\n")
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/m", "--cwd", dir, "-c", cfg, "--no-stream"); code == 0 || f.count() != 0 || !strings.Contains(stderr, "base_ur") {
		t.Errorf("misspelled key: exit %d, stderr %q", code, stderr)
	}
}

// TestProviderPricing covers AC7 and AC9 through the CLI.
func TestProviderPricing(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	big := `{"choices":[{"message":{"role":"assistant","content":"thinking"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":1000}}`
	f := &fakeGateway{replies: []string{big}}
	srv := f.start(t)
	base := "providers:\n  - id: gw\n    api: chat\n    base_url: " + srv.URL + "\n    models:\n      m1: {input_price_per_mtok: 10, output_price_per_mtok: 10}\n"
	traj := filepath.Join(t.TempDir(), "run.json")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "gw/m1", "--cwd", dir, "-c", providerCfg(t, dir, base), "--no-stream", "-o", traj)
	data, _ := os.ReadFile(traj)
	// 1000 in + 1000 out at $10/MTok each is $0.02.
	if code != 0 || strings.Contains(stderr, "no pricing available") || !strings.Contains(string(data), `"cost": 0.02,`) {
		t.Errorf("configured price: exit %d, stderr %q\n%s", code, stderr, data)
	}
	// A model without prices warns.
	_, _, stderr = runCLI(t, "-t", "x", "-m", "gw/unpriced", "--cwd", dir, "-c", providerCfg(t, dir, base), "--no-stream", "--cost-limit", "0.01", "--step-limit", "1")
	if !strings.Contains(stderr, "no pricing available for gw/unpriced") {
		t.Errorf("no unpriced warning: %q", stderr)
	}
	// Top-level prices win: $0.001/MTok each gives $0.000002.
	traj = filepath.Join(t.TempDir(), "run2.json")
	runCLI(t, "-t", "x", "-m", "gw/m1", "--cwd", dir, "-c", providerCfg(t, dir, base+"input_price_per_mtok: 0.001\noutput_price_per_mtok: 0.001\n"), "--no-stream", "-o", traj)
	if data, _ := os.ReadFile(traj); !strings.Contains(string(data), `"cost": 0.000002,`) {
		t.Errorf("top-level override not used:\n%s", data)
	}
}

// TestProviderUnknownID covers AC11.
func TestProviderUnknownID(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	cfg := providerCfg(t, dir, "providers:\n  - id: gw\n    api: chat\n    base_url: http://127.0.0.1:1\n")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "nope/x", "--cwd", dir, "-c", cfg, "--no-stream")
	if code == 0 || !strings.Contains(stderr, "openai, openrouter, anthropic, ollama, codex, claude-code, codex-exec; configured: gw") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// TestProviderPlainHTTPWarning covers the non-loopback http warning.
func TestProviderPlainHTTPWarning(t *testing.T) {
	model.SetProviders([]model.ProviderDef{
		{ID: "gw", API: "chat", BaseURL: "http://gw.example.com"},
		{ID: "loc", API: "chat", BaseURL: "http://localhost:8080"},
	})
	t.Cleanup(func() { model.SetProviders(nil) })
	var b strings.Builder
	announceProvider(&b, "gw/m", false)
	if !strings.Contains(b.String(), `warning: provider "gw" uses plain http`) {
		t.Errorf("no warning: %q", b.String())
	}
	b.Reset()
	announceProvider(&b, "loc/m", false)
	announceProvider(&b, "openai/gpt-5", true)
	if b.String() != "" {
		t.Errorf("unexpected output: %q", b.String())
	}
}

// TestProviderResume covers AC12.
func TestProviderResume(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	readA := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	f := &fakeGateway{replies: []string{readA, chatSubmit}}
	srv := f.start(t)
	entry := "providers:\n  - id: gateway\n    api: chat\n    base_url: " + srv.URL + "\n"
	cfg := providerCfg(t, dir, entry)
	traj := filepath.Join(t.TempDir(), "run.json")
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "gateway/m1", "--cwd", dir, "-c", cfg, "--no-stream", "--step-limit", "1", "-o", traj); f.count() != 1 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	data, _ := os.ReadFile(traj)
	if !strings.Contains(string(data), `"provider_api": "chat"`) || !strings.Contains(string(data), `"provider_base_url": "`+srv.URL+`"`) {
		t.Fatalf("trajectory lacks provider fields:\n%s", data)
	}
	empty := filepath.Join(dir, "none.yaml")
	for name, c := range map[string]struct{ cfg, want string }{
		"missing":      {empty, "not defined"},
		"api changed":  {providerCfg(t, dir, "providers:\n  - id: gateway\n    api: responses\n    base_url: "+srv.URL+"\n"), "api changed"},
		"base changed": {providerCfg(t, dir, "providers:\n  - id: gateway\n    api: chat\n    base_url: http://127.0.0.1:9\n"), "base_url changed"},
	} {
		code, _, stderr := runCLI(t, "--resume", traj, "--cwd", dir, "-c", c.cfg, "--no-stream", "-o", filepath.Join(t.TempDir(), "x.json"))
		if code == 0 || f.count() != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, %d requests, stderr %q", name, code, f.count(), stderr)
		}
	}
	next := filepath.Join(t.TempDir(), "next.json")
	code, _, stderr := runCLI(t, "--resume", traj, "--cwd", dir, "-c", providerCfg(t, dir, entry), "--no-stream", "-o", next)
	if code != 0 || f.count() != 2 || f.paths[1] != "/chat/completions" {
		t.Fatalf("resume: exit %d, %d requests; stderr:\n%s", code, f.count(), stderr)
	}
	if data, _ := os.ReadFile(next); !strings.Contains(string(data), `"provider_api": "chat"`) {
		t.Error("resumed trajectory lacks provider_api")
	}
}

// TestBuiltinRunStartedHasNoProviderFields covers AC13's built-in half.
func TestBuiltinRunStartedHasNoProviderFields(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	f := &fakeGateway{replies: []string{chatSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "-c", emptyCfg, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	ev := runStarted(t, stdout)
	if _, ok := ev["provider_api"]; ok {
		t.Error("built-in run.started has provider_api")
	}
	if _, ok := ev["provider_base_url"]; ok {
		t.Error("built-in run.started has provider_base_url")
	}
}
