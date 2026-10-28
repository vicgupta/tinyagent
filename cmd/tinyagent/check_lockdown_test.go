package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLockdownConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckLockdownStatus(t *testing.T) {
	hits := checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	cwd := t.TempDir()
	base := []string{"--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", cwd}

	code, stdout, stderr := runCLI(t, base...)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, "lockdown:") {
		t.Errorf("lockdown line with lockdown off:\n%s", stdout)
	}

	code, stdout, _ = runCLI(t, append(base, "--lockdown=false")...)
	if code != 0 || reportLine(t, stdout, "lockdown") != "off (from --lockdown)" {
		t.Errorf("--lockdown=false: exit %d\n%s", code, stdout)
	}

	code, stdout, stderr = runCLI(t, append(base, "--lockdown")...)
	if code != 0 {
		t.Fatalf("--lockdown: exit %d; stderr:\n%s", code, stderr)
	}
	want := "on (from --lockdown) (sources: web_fetch, mcp, review, untrusted task; after taint: commands refused, web_fetch → 0 domains, mcp tools refused)"
	if got := reportLine(t, stdout, "lockdown"); got != want {
		t.Errorf("--lockdown line\n got %q\nwant %q", got, want)
	}

	cfg := writeLockdownConfig(t, `untrusted_content:
  enabled: true
  sources: [web_fetch]
  lockdown:
    web_fetch_allowed_domains: [docs.example.com]
mcp_servers:
  - alias: docs
    command: docs-server
    tools: [search]
    untrusted: false
    lockdown_allowed: true
  - alias: issues
    command: issues-server
    tools: [read]
`)
	code, stdout, stderr = runCLI(t, append(base, "-c", cfg, "--untrusted-task")...)
	if code != 0 {
		t.Fatalf("config: exit %d; stderr:\n%s", code, stderr)
	}
	got := reportLine(t, stdout, "lockdown")
	for _, part := range []string{
		"on (from config)",
		"sources: web_fetch, untrusted task;",
		"web_fetch → 1 domains",
		"mcp → docs",
		"mcp results that don't taint: docs",
		"web_fetch after taint: docs.example.com",
		"run starts tainted (--untrusted-task)",
	} {
		if !strings.Contains(got, part) {
			t.Errorf("config line %q missing %q", got, part)
		}
	}

	code, stdout, _ = runCLI(t, append(base, "-c", cfg, "--lockdown=false")...)
	if code != 0 || reportLine(t, stdout, "lockdown") != "off (from --lockdown)" {
		t.Errorf("config with --lockdown=false: exit %d\n%s", code, stdout)
	}

	code, _, stderr = runCLI(t, append(base, "--untrusted-task")...)
	if code != 2 || !strings.Contains(stderr, "--untrusted-task requires lockdown") {
		t.Errorf("--untrusted-task without lockdown: exit %d, stderr %q", code, stderr)
	}

	bad := writeLockdownConfig(t, "untrusted_content:\n  enabled: true\n  sources: [carrier_pigeon]\n")
	if code, _, stderr := runCLI(t, append(base, "-c", bad)...); code != 2 || !strings.Contains(stderr, "carrier_pigeon") {
		t.Errorf("invalid sources: exit %d, stderr %q", code, stderr)
	}
	if hits.Load() != 0 {
		t.Errorf("--check made %d requests", hits.Load())
	}
}
