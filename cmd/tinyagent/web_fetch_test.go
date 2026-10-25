package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWebFetchFlagAndConfig covers enabling the tool (AC1, AC2, AC3, AC9).
func TestWebFetchFlagAndConfig(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	onCfg := filepath.Join(dir, "on.yaml")
	os.WriteFile(onCfg, []byte("web_fetch:\n  enabled: true\n  allowed_domains: [pkg.go.dev, \"*.python.org\"]\n"), 0o644)
	listCfg := filepath.Join(dir, "list.yaml")
	os.WriteFile(listCfg, []byte("web_fetch:\n  allowed_domains: [pkg.go.dev]\n"), 0o644)
	for _, c := range []struct {
		name, line string
		want       bool
		args       []string
	}{
		{"default", "", false, []string{"-c", emptyCfg}},
		{"config", "web_fetch: 2 domains (pkg.go.dev, *.python.org)", true, []string{"-c", onCfg}},
		{"flag", "web_fetch: 1 domain (pkg.go.dev)", true, []string{"-c", listCfg, "--web-fetch"}},
		{"flag list replaces config", "web_fetch: 2 domains (docs.rs, go.dev)", true,
			[]string{"-c", onCfg, "--web-fetch-allow-domain", "docs.rs", "--web-fetch-allow-domain", "go.dev"}},
		{"flag beats config", "", false, []string{"-c", onCfg, "--web-fetch=false"}},
		{"read-only", "web_fetch: 2 domains", true, []string{"-c", onCfg, "--read-only"}},
		{"sandbox setting does not matter", "web_fetch: 2 domains", true, []string{"-c", onCfg, "--sandbox=false"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResponses{replies: []string{responsesSubmit}}
			t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--openai-api", "responses", "--no-stream"}, c.args...)...)
			})
			if code != 0 || len(f.bodies) != 1 {
				t.Fatalf("exit %d, %d requests; stderr:\n%s", code, len(f.bodies), stderr)
			}
			names := sentToolNames(f.bodies[0])
			if got := slices.Contains(names, "web_fetch"); got != c.want {
				t.Errorf("web_fetch sent = %v, want %v (tools %v)", got, c.want, names)
			}
			if c.line != "" && !strings.Contains(stderr, c.line) {
				t.Errorf("stderr lacks %q:\n%s", c.line, stderr)
			}
			if c.line == "" && strings.Contains(stderr, "web_fetch") {
				t.Errorf("stderr mentions web_fetch while off:\n%s", stderr)
			}
		})
	}
}

// TestWebFetchInvalidSettingsExit2 covers AC2's validation.
func TestWebFetchInvalidSettingsExit2(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	cfg := func(name, data string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(data), 0o644)
		return p
	}
	for _, c := range []struct {
		name, want string
		args       []string
	}{
		{"empty allowlist", "web_fetch.allowed_domains is empty", []string{"-c", emptyCfg, "--web-fetch"}},
		{"bare star", "web_fetch.allowed_domains", []string{"-c", cfg("star.yaml", "web_fetch: {enabled: true, allowed_domains: [\"*\"]}\n")}},
		{"bare star flag", "*", []string{"-c", emptyCfg, "--web-fetch", "--web-fetch-allow-domain", "*"}},
		{"timeout 0", "web_fetch.timeout_seconds", []string{"-c", cfg("t0.yaml", "web_fetch: {timeout_seconds: 0}\n")}},
		{"timeout 121", "web_fetch.timeout_seconds", []string{"-c", cfg("t121.yaml", "web_fetch: {timeout_seconds: 121}\n")}},
		{"max_bytes 0", "web_fetch.max_bytes", []string{"-c", cfg("mb.yaml", "web_fetch: {max_bytes: 0}\n")}},
		{"allow-domain without enabling", "--web-fetch-allow-domain requires --web-fetch", []string{"-c", emptyCfg, "--web-fetch-allow-domain", "go.dev"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResponses{replies: []string{responsesSubmit}}
			t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
			var code int
			var errOut string
			stderr := captureStderr(t, func() {
				code, _, errOut = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--openai-api", "responses", "--no-stream"}, c.args...)...)
			})
			stderr += errOut
			if code != 2 || !strings.Contains(stderr, c.want) || len(f.bodies) != 0 {
				t.Errorf("exit %d, %d requests, stderr:\n%s", code, len(f.bodies), stderr)
			}
		})
	}
}

func TestResumeWebFetchSettingMustMatch(t *testing.T) { // AC8
	tr, source, output, cfg := resumeFixture(t)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--web-fetch")
	})
	if code != 2 || !strings.Contains(stderr, "--web-fetch conflicts with the checkpoint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
