package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWebSearchFlagAndConfig covers the on/off/lockdown matrix.
func TestWebSearchFlagAndConfig(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(data), 0o644)
		return p
	}
	off := write("off.yaml", "web_search:\n  enabled: false\n")
	searx := write("searx.yaml", "web_search:\n  searxng_url: http://127.0.0.1:8888\n")
	order := write("order.yaml", "web_search:\n  providers: [exa, duckduckgo]\n")
	for _, c := range []struct {
		name, line string
		want       bool
		args       []string
	}{
		{"default", "web_search: duckduckgo → exa", true, []string{"-c", emptyCfg}},
		{"no-web-search", "", false, []string{"-c", emptyCfg, "--no-web-search"}},
		{"web-search=false", "", false, []string{"-c", emptyCfg, "--web-search=false"}},
		{"config off", "", false, []string{"-c", off}},
		{"flag beats config", "web_search: duckduckgo → exa", true, []string{"-c", off, "--web-search"}},
		{"lockdown default off", "", false, []string{"-c", emptyCfg, "--lockdown"}},
		{"lockdown explicit on", "web_search: duckduckgo → exa", true, []string{"-c", emptyCfg, "--lockdown", "--web-search"}},
		{"read-only keeps it", "web_search: duckduckgo → exa", true, []string{"-c", emptyCfg, "--read-only"}},
		{"searxng first when configured", "web_search: searxng → duckduckgo → exa", true, []string{"-c", searx}},
		{"custom order", "web_search: exa → duckduckgo", true, []string{"-c", order}},
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
			if got := slices.Contains(names, "web_search"); got != c.want {
				t.Errorf("web_search sent = %v, want %v (tools %v)", got, c.want, names)
			}
			if c.line != "" && !strings.Contains(stderr, c.line+"\n") {
				t.Errorf("stderr lacks %q:\n%s", c.line, stderr)
			}
			if c.line == "" && strings.Contains(stderr, "web_search:") {
				t.Errorf("stderr mentions web_search while off:\n%s", stderr)
			}
		})
	}
}

func TestWebSearchInvalidSettingsExit2(t *testing.T) {
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
		{"flags conflict", "--web-search and --no-web-search conflict", []string{"-c", emptyCfg, "--web-search", "--no-web-search"}},
		{"unknown provider", "unknown provider \"brave\"", []string{"-c", cfg("p.yaml", "web_search: {providers: [brave]}\n")}},
		{"duplicate", "duplicate provider", []string{"-c", cfg("d.yaml", "web_search: {providers: [exa, exa]}\n")}},
		{"empty list", "web_search.providers is empty", []string{"-c", cfg("e.yaml", "web_search: {providers: []}\n")}},
		{"searxng without url", "searxng_url is not set", []string{"-c", cfg("s.yaml", "web_search: {providers: [searxng]}\n")}},
		{"bad url", "web_search.searxng_url", []string{"-c", cfg("u.yaml", "web_search: {searxng_url: \"ftp://x\"}\n")}},
		{"max_results", "web_search.max_results", []string{"-c", cfg("m.yaml", "web_search: {max_results: 11}\n")}},
		{"timeout", "web_search.timeout_seconds", []string{"-c", cfg("t.yaml", "web_search: {timeout_seconds: 0}\n")}},
		{"key slot rejected", "api_key", []string{"-c", cfg("k.yaml", "web_search: {api_key: x}\n")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir}, c.args...)...)
			})
			want := 2
			if c.name == "key slot rejected" {
				want = 1 // unknown keys fail parsing
			}
			if code != want || !strings.Contains(stderr, c.want) {
				t.Fatalf("exit %d, want %d with %q:\n%s", code, want, c.want, stderr)
			}
		})
	}
}

// TestResumeWebSearchSettingMustMatch: a checkpoint without
// web_search_enabled had web_search off, so --web-search conflicts and a
// plain resume keeps it off.
func TestResumeWebSearchSettingMustMatch(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--web-search")
	})
	if code != 2 || !strings.Contains(stderr, "--web-search conflicts with the checkpoint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
