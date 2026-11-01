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
)

// skillsServer answers every request with a submission and records whether
// the last request offered the skill tool.
func skillsServer(t *testing.T, offered *atomic.Bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		offered.Store(strings.Contains(string(b), `"name":"skill"`))
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func skillsRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for path, body := range map[string]string{
		".git/HEAD":               "",
		"skills/release/SKILL.md": "---\nname: release\ndescription: Cut a release.\n---\nsteps\n",
		"skills/broken/SKILL.md":  "---\nname: broken\n---\nno description\n",
	} {
		p := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runSkills(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			var flagErr string
			code, _, flagErr = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir}, args...)...)
			os.Stderr.WriteString(flagErr)
		})
	})
	return
}

func TestSkillsFlagsAndConfig(t *testing.T) { // AC10
	var offered atomic.Bool
	skillsServer(t, &offered)
	dir := skillsRepo(t)
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cfg, []byte("skills: true\n"), 0o644)
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--skills"}, true},
		{[]string{"--no-skills=false"}, true},
		{[]string{"-c", cfg}, true},
		{[]string{"-c", cfg, "--no-skills"}, false},
		{[]string{"-c", cfg, "--skills=false"}, false},
	} {
		offered.Store(false)
		code, _, stderr := runSkills(t, dir, c.args...)
		if code != 0 || offered.Load() != c.want {
			t.Errorf("%v: exit %d, offered %v, stderr %q", c.args, code, offered.Load(), stderr)
		}
	}
	if code, _, stderr := runSkills(t, dir, "--skills", "--no-skills"); code == 0 || !strings.Contains(stderr, "conflict") {
		t.Errorf("conflict: exit %d, stderr %q", code, stderr)
	}
}

func TestSkillsWarningsInJSON(t *testing.T) { // AC14
	var offered atomic.Bool
	skillsServer(t, &offered)
	code, stdout, _ := runSkills(t, skillsRepo(t), "--skills", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var warnings []string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stdout line is not JSON: %q", line)
		}
		if ev["type"] == "warning" {
			warnings = append(warnings, ev["message"].(string))
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "broken") || !strings.Contains(warnings[0], "description") {
		t.Errorf("warnings = %q", warnings)
	}
}
