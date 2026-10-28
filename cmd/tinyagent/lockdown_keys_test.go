package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A typo under untrusted_content exits 2 before any model request, in the
// main config and in a profile, and in --check too.
func TestLockdownUnknownKeyExits2(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
		args             []string
	}{
		{"top level", "untrusted_content:\n  enable: true\n", "untrusted_content: yaml: unmarshal errors:\n  field enable not found", []string{"-t", "x"}},
		{"lockdown", "untrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domains: [proxy.golang.org]\n", "field allowed_domains not found in type config.UntrustedLockdown", []string{"-t", "x"}},
		{"profile", "profiles:\n  ci:\n    untrusted_content:\n      enable: true\n", "profile ci", []string{"-t", "x", "--profile", "ci"}},
		{"check", "untrusted_content:\n  enable: true\n", "untrusted_content: yaml: unmarshal errors:\n  field enable not found", []string{"--check"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, requests := lockdownEnv(t)
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfgPath}, c.args...)
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, args...)
			})
			if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, c.want) || strings.Contains(stderr, "line ") {
				t.Errorf("exit %d, requests %d, stderr %q", code, requests.Load(), stderr)
			}
		})
	}
}
