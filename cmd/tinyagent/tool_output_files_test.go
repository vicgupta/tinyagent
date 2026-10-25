package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// fakeBashThenDone replies with one bash call printing many lines, then a
// submission, for every pair of requests.
func fakeBashThenDone(t *testing.T, requests *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n%2 == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_`+string(rune('0'+n))+`","type":"function","function":{"name":"bash","arguments":"{\"command\":\"seq 1 3000\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func toolOutputSetup(t *testing.T, cfgText string) (root, cfg string) {
	t.Helper()
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	cfg = filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(cfg, []byte("max_output_chars: 1000\n"+cfgText), 0o644)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	return root, cfg
}

// jsonEvents returns the decoded --json events of a given type.
func jsonEvents(t *testing.T, stdout, typ string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		if e["type"] == typ {
			out = append(out, e)
		}
	}
	return out
}

func runToolOutput(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var requests atomic.Int32
	t.Setenv("OPENAI_BASE_URL", fakeBashThenDone(t, &requests).URL)
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, append([]string{"--no-stream", "--json", "-m", "openai/gpt-4o"}, args...)...)
		})
	})
	return code, stdout, stderr
}

func TestToolOutputFilesCLI(t *testing.T) { // AC2, AC11
	for _, c := range []struct {
		name, cfg string
		args      []string
		want      bool
	}{
		{"default off", "", nil, false},
		{"flag", "", []string{"--tool-output-files"}, true},
		{"config", "tool_output_files:\n  enabled: true\n", nil, true},
		{"flag beats config", "tool_output_files:\n  enabled: true\n", []string{"--tool-output-files=false"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, cfg := toolOutputSetup(t, c.cfg)
			traj := filepath.Join(t.TempDir(), "run.json")
			code, stdout, stderr := runToolOutput(t, append([]string{"-t", "x", "--cwd", root, "-c", cfg, "-o", traj}, c.args...)...)
			if code != 0 {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			results := jsonEvents(t, stdout, "tool.result")
			if len(results) != 1 {
				t.Fatalf("tool.result events: %v", results)
			}
			file, _ := results[0]["output_file"].(string)
			tr, err := trajectory.Load(traj)
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(filepath.Join(root, ".tinyagent"))
			if !c.want {
				if file != "" || results[0]["output_bytes"] != nil || tr.ToolOutputFiles != nil || !os.IsNotExist(statErr) {
					t.Errorf("feature off: event %v, trajectory %v, stat %v", results[0], tr.ToolOutputFiles, statErr)
				}
				return
			}
			data, err := os.ReadFile(filepath.Join(root, file))
			if err != nil || results[0]["output_bytes"] != float64(len(data)) {
				t.Fatalf("event %v; file %v", results[0], err)
			}
			if len(tr.ToolOutputFiles) != 1 || tr.ToolOutputFiles[0].OutputFile != file || tr.ToolOutputFiles[0].OutputBytes != len(data) {
				t.Errorf("trajectory = %+v", tr.ToolOutputFiles)
			}
			if out := results[0]["output"].(string); len(out) > 1000 || !strings.Contains(out, "saved to "+file) {
				t.Errorf("output (%d bytes) = %q", len(out), out)
			}
		})
	}
}

func TestToolOutputFilesResumeToggled(t *testing.T) { // AC11
	root, cfg := toolOutputSetup(t, "")
	traj := filepath.Join(t.TempDir(), "run.json")
	if code, _, stderr := runToolOutput(t, "-t", "x", "--cwd", root, "-c", cfg, "-o", traj, "--tool-output-files"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, c := range []struct {
		name string
		args []string
		want int
	}{{"off", nil, 1}, {"on", []string{"--tool-output-files"}, 2}} {
		t.Run(c.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "next.json")
			code, stdout, stderr := runToolOutput(t, append([]string{"--resume", traj, "-t", "again", "--cwd", root, "-c", cfg, "-o", out}, c.args...)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			tr, err := trajectory.Load(out)
			if err != nil || len(tr.ToolOutputFiles) != c.want {
				t.Errorf("resumed trajectory entries = %+v, %v; want %d", tr.ToolOutputFiles, err, c.want)
			}
			if _, ok := jsonEvents(t, stdout, "tool.result")[0]["output_file"]; ok != (c.want == 2) {
				t.Errorf("resume %s: tool.result output_file present = %v", c.name, ok)
			}
		})
	}
}

func TestToolOutputFilesDirOutsideCwd(t *testing.T) { // AC9
	root, cfg := toolOutputSetup(t, "tool_output_files:\n  enabled: true\n  dir: ../outside\n")
	var requests atomic.Int32
	t.Setenv("OPENAI_BASE_URL", fakeBashThenDone(t, &requests).URL)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "--no-stream", "-m", "openai/gpt-4o", "--cwd", root, "-c", cfg)
	})
	if code == 0 || requests.Load() != 0 || !strings.Contains(stderr, "tool_output_files.dir") {
		t.Errorf("exit %d, %d requests; stderr:\n%s", code, requests.Load(), stderr)
	}
	// An absolute dir outside --cwd passes config loading but not startup.
	os.WriteFile(cfg, []byte("tool_output_files:\n  dir: "+t.TempDir()+"\n"), 0o644)
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "--no-stream", "-m", "openai/gpt-4o", "--cwd", root, "-c", cfg, "--tool-output-files")
	})
	if code == 0 || requests.Load() != 0 || !strings.Contains(stderr, "tool_output_files.dir") {
		t.Errorf("absolute: exit %d, %d requests; stderr:\n%s", code, requests.Load(), stderr)
	}
}

func TestToolOutputFilesWriteFailureWarnsOnce(t *testing.T) { // AC8
	root, cfg := toolOutputSetup(t, "")
	os.WriteFile(filepath.Join(root, ".tinyagent"), []byte("not a dir"), 0o644)
	code, stdout, stderr := runToolOutput(t, "-t", "x", "--cwd", root, "-c", cfg, "--tool-output-files")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := strings.Count(stderr, "tool_output_files: could not save"); n != 1 {
		t.Errorf("%d warnings on stderr:\n%s", n, stderr)
	}
	warnings := jsonEvents(t, stdout, "warning")
	if len(warnings) != 1 || !strings.Contains(warnings[0]["message"].(string), "tool_output_files") {
		t.Errorf("warning events = %v", warnings)
	}
	res := jsonEvents(t, stdout, "tool.result")[0]
	if _, ok := res["output_file"]; ok || !strings.Contains(res["output"].(string), "characters elided") || res["is_error"] != false {
		t.Errorf("tool.result = %v", res)
	}
	if done := jsonEvents(t, stdout, "result"); len(done) != 1 || done[0]["status"] != "Submitted" {
		t.Errorf("result = %v", done)
	}
}

func TestToolOutputFilesPruneAtStart(t *testing.T) { // AC10
	root, cfg := toolOutputSetup(t, "tool_output_files:\n  max_runs: 2\n")
	dir := filepath.Join(root, ".tinyagent", "tool-output")
	old := []string{"20260101T000000.000Z-aaaaaa", "20260102T000000.000Z-bbbbbb", "20260103T000000.000Z-cccccc"}
	for _, id := range old {
		os.MkdirAll(filepath.Join(dir, id), 0o700)
		os.WriteFile(filepath.Join(dir, id, "c.txt"), []byte("x"), 0o600)
	}
	if code, _, stderr := runToolOutput(t, "-t", "x", "--cwd", root, "-c", cfg, "--tool-output-files"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 3 || names[0] != old[1] || names[1] != old[2] {
		t.Errorf("run dirs after prune = %v", names)
	}
}
