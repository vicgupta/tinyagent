package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestSandboxBackendFlagValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{"firejail", ""} {
		if code, _, got := runWith(t, "--sandbox", "--sandbox-backend", v, "-t", "x"); code != 2 || !strings.Contains(got, "must be auto, landlock or bwrap") {
			t.Errorf("--sandbox-backend %q: exit %d, stderr %q", v, code, got)
		}
	}
}

func TestSandboxBackendFlagOverridesConfig(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"--sandbox", "--sandbox-backend", "landlock"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.AgentConfig{SandboxBackend: "bwrap"}
	applySandboxFlags(fs, &cfg, o)
	if cfg.SandboxBackend != "landlock" || !cfg.Sandbox {
		t.Errorf("cfg = %+v", cfg)
	}
}

// fakeSandboxProbe replaces the --check sandbox probe.
func fakeSandboxProbe(t *testing.T, desc string, warnings []string, err error) *string {
	t.Helper()
	old := probeSandbox
	t.Cleanup(func() { probeSandbox = old })
	var got string
	probeSandbox = func(backend string, network bool, allowed []string) (string, []string, error) {
		got = backend
		return desc, warnings, err
	}
	return &got
}

func TestCheckSandboxLine(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "x")
	dir := t.TempDir()
	args := []string{"--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", dir}

	code, stdout, stderr := runCLI(t, args...)
	if code != 0 || strings.Contains(stdout, "sandbox:") {
		t.Fatalf("no --sandbox: exit %d, sandbox line in:\n%s%s", code, stdout, stderr)
	}

	backend := fakeSandboxProbe(t, "bwrap + landlock (ABI 6)", nil, nil)
	code, stdout, stderr = runCLI(t, append(args, "--sandbox")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := reportLine(t, stdout, "sandbox"); got != "bwrap + landlock (ABI 6) (sandbox_backend auto)" || *backend != "auto" {
		t.Errorf("sandbox line %q, backend %q", got, *backend)
	}

	cfg := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cfg, []byte("sandbox: true\nsandbox_backend: bwrap\n"), 0o600)
	backend = fakeSandboxProbe(t, "landlock (ABI 6)", []string{"bwrap unavailable, using Landlock alone: AppArmor restricts unprivileged user namespaces"}, nil)
	code, stdout, stderr = runCLI(t, append(args, "-c", cfg, "--sandbox-backend", "auto")...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := reportLine(t, stdout, "sandbox"); !strings.Contains(got, "landlock (ABI 6) (sandbox_backend auto); warning: bwrap unavailable") || *backend != "auto" {
		t.Errorf("sandbox line %q", got)
	}

	fakeSandboxProbe(t, "", nil, errors.New("sandbox: bwrap can't create its sandbox: AppArmor restricts unprivileged user namespaces"))
	code, stdout, stderr = runCLI(t, append(args, "-c", cfg)...)
	if got := reportLine(t, stdout, "sandbox"); code != 0 || !strings.HasPrefix(got, "unavailable (sandbox_backend bwrap): ") || !strings.Contains(got, "AppArmor") {
		t.Errorf("bwrap blocked: exit %d, sandbox line %q, stderr %q", code, got, stderr)
	}
}
