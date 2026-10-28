package main

import (
	"strings"
	"testing"
)

// --check reports that sandboxed commands keep running after taint, with
// network denied or narrowed to untrusted_content.lockdown.allowed_domains.
func TestCheckLockdownSandboxedCommands(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	base := []string{"--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir()}
	for _, c := range []struct{ name, body, want string }{
		{"denied", "sandbox: true\nuntrusted_content:\n  enabled: true\n", "after taint: commands sandboxed, network denied,"},
		{"proxied", "sandbox: true\nsandbox_allowed_domains: [proxy.golang.org, github.com]\nuntrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domains: [proxy.golang.org]\n", "after taint: commands sandboxed, network → 1 domain (proxy.golang.org),"},
		{"sandbox_network", "sandbox: true\nsandbox_network: true\nuntrusted_content:\n  enabled: true\n", "after taint: commands refused,"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, append(base, "--config", writeLockdownConfig(t, c.body))...)
			if code != 0 {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			if got := reportLine(t, stdout, "lockdown"); !strings.Contains(got, c.want) {
				t.Errorf("lockdown line %q, want %q", got, c.want)
			}
		})
	}
	code, _, stderr := runCLI(t, append(base, "--config", writeLockdownConfig(t, "untrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domains: [\"*\"]\n"))...)
	if code != 2 || !strings.Contains(stderr, "untrusted_content.lockdown.allowed_domains") {
		t.Errorf("bad allowed_domains: exit %d, stderr %q", code, stderr)
	}
}
