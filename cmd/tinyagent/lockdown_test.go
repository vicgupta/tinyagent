package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
)

// captureStdoutStderr runs f with os.Stdout and os.Stderr redirected.
func captureStdoutStderr(t *testing.T, f func()) (stdout, stderr string) {
	t.Helper()
	var out string
	stderr = captureStderr(t, func() {
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
		out = <-done
	})
	return out, stderr
}

func lockdownEnv(t *testing.T) (dir string, requests *atomic.Int32) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // os.UserConfigDir ignores XDG_CONFIG_HOME on macOS
	t.Setenv("AGENT_MODEL", "")
	requests = &atomic.Int32{}
	srv := fakeOpenAI(t, requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	return t.TempDir(), requests
}

func TestUntrustedTaskRequiresLockdown(t *testing.T) {
	dir, requests := lockdownEnv(t)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--untrusted-task")
	})
	if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, "--untrusted-task requires lockdown") {
		t.Errorf("exit %d, requests %d, stderr %q", code, requests.Load(), stderr)
	}
}

func TestLockdownUntrustedTaskRun(t *testing.T) {
	dir, requests := lockdownEnv(t)
	var code int
	stdout, stderr := captureStdoutStderr(t, func() {
		code, _, _ = runWith(t, "-t", "issue body", "-m", "openai/gpt-4o", "--no-stream", "--json", "--cwd", dir, "--lockdown", "--untrusted-task")
	})
	if code != 0 || requests.Load() != 1 {
		t.Fatalf("exit %d, requests %d, stderr %q", code, requests.Load(), stderr)
	}
	if !strings.Contains(stderr, "lockdown: on (") || !strings.Contains(stderr, "lockdown: run starts tainted (untrusted_task)") {
		t.Errorf("stderr = %q", stderr)
	}
	var taints, started int
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event %q: %v", line, err)
		}
		switch ev["type"] {
		case "run.started":
			started++
		case "taint":
			if started == 0 || ev["source"] != "untrusted_task" {
				t.Errorf("taint event %v (after run.started: %v)", ev, started > 0)
			}
			taints++
		}
	}
	if taints != 1 {
		t.Errorf("events = %s", stdout)
	}
}

func TestLockdownOffByDefaultRun(t *testing.T) {
	dir, requests := lockdownEnv(t)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir)
	})
	if code != 0 || requests.Load() != 1 || strings.Contains(stderr, "lockdown") {
		t.Errorf("exit %d, requests %d, stderr %q", code, requests.Load(), stderr)
	}
}

func TestLockdownBadConfigExits2(t *testing.T) {
	dir, requests := lockdownEnv(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("untrusted_content:\n  enabled: true\n  lockdown:\n    web_fetch_allowed_domains: [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfgPath)
	})
	if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, "untrusted_content.lockdown.web_fetch_allowed_domains") {
		t.Errorf("exit %d, requests %d, stderr %q", code, requests.Load(), stderr)
	}
}

func TestSetupLockdown(t *testing.T) {
	parse := func(args ...string) (*options, func() (*lockdown.State, int, string, config.AgentConfig)) {
		o := &options{}
		fs := newFlagSet(o)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return o, func() (*lockdown.State, int, string, config.AgentConfig) {
			cfg := config.Default()
			if len(args) > 0 && args[0] == "cfg-on" {
				cfg.UntrustedContent.Enabled = true
			}
			var buf bytes.Buffer
			st, code := setupLockdown(fs, o.lockdown, &cfg, false, false, &buf)
			return st, code, buf.String(), cfg
		}
	}
	_, run := parse()
	if st, code, _, _ := run(); st != nil || code != 0 {
		t.Error("lockdown on by default")
	}
	_, run = parse("--lockdown")
	if st, code, _, cfg := run(); st == nil || code != 0 || st.Tainted() || !cfg.UntrustedContent.Enabled {
		t.Error("--lockdown didn't turn it on untainted")
	}
	_, run = parse("--lockdown", "--untrusted-task")
	if st, _, _, _ := run(); !st.Tainted() {
		t.Error("--untrusted-task didn't taint")
	}
	_, run = parse("--lockdown=false", "--untrusted-task")
	if _, code, msg, _ := run(); code != 2 || !strings.Contains(msg, "requires lockdown") {
		t.Errorf("--lockdown=false --untrusted-task: code %d %q", code, msg)
	}

	o := &options{}
	fs := newFlagSet(o)
	cfg := config.Default()
	cfg.UntrustedContent.Enabled = true
	st, code := setupLockdown(fs, o.lockdown, &cfg, true, true, io.Discard)
	src := st.Sources()
	if code != 0 || len(src) != 2 || src[0].Source != lockdown.SourceReview || src[1].Source != lockdown.SourceResume {
		t.Errorf("review/resume taint = %+v", src)
	}
	if err := fs.Parse([]string{"--lockdown=false"}); err != nil {
		t.Fatal(err)
	}
	if st, _ := setupLockdown(fs, o.lockdown, &cfg, true, true, io.Discard); st != nil {
		t.Error("--lockdown=false didn't override the config")
	}
}
