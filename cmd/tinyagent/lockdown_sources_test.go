package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
)

func TestSetupLockdownSources(t *testing.T) {
	o := &options{}
	fs := newFlagSet(o)
	cfg := config.Default()
	cfg.UntrustedContent.Enabled = true
	cfg.UntrustedContent.Sources = []string{lockdown.SourceWebFetch}
	var buf bytes.Buffer
	st, code := setupLockdown(fs, o.lockdown, &cfg, true, false, &buf)
	if code != 0 || st == nil || st.Tainted() {
		t.Fatalf("review run tainted with sources: [web_fetch]: code %d %q", code, buf.String())
	}
	if !strings.Contains(st.Describe(), "sources: web_fetch, untrusted task;") {
		t.Errorf("describe = %q", st.Describe())
	}
	cfg.UntrustedContent.Sources = []string{lockdown.SourceReview}
	if st, _ := setupLockdown(fs, o.lockdown, &cfg, true, false, &buf); !st.Tainted() {
		t.Error("review run not tainted with sources: [review]")
	}
	cfg.UntrustedContent.Sources = []string{"nope"}
	buf.Reset()
	if st, code := setupLockdown(fs, o.lockdown, &cfg, false, false, &buf); st != nil || code != 2 || !strings.Contains(buf.String(), "untrusted_content.sources") {
		t.Errorf("bad sources: st %v code %d %q", st, code, buf.String())
	}
}

func TestLockdownBadSourcesExits2(t *testing.T) {
	dir, requests := lockdownEnv(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("untrusted_content:\n  enabled: true\n  sources: [web-fetch]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfgPath)
	})
	if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, "untrusted_content.sources") {
		t.Errorf("exit %d, requests %d, stderr %q", code, requests.Load(), stderr)
	}
}
