package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --check predicts the container's network mode from container.run_args
// (and podman's rootless default) without starting a container.
func TestCheckLockdownContainerNetworkMode(t *testing.T) {
	cases := []struct {
		name, container, want, notWant string
	}{
		{"docker default", "  image: golang:1.23\n",
			"commands in container (network removed at taint), network denied,", ""},
		{"docker default note", "  image: golang:1.23\n",
			"; container network bridge (runtime default)", ""},
		{"none", "  image: golang:1.23\n  run_args: [\"--network\", \"none\"]\n",
			"after taint: commands in container, network denied,", "removed at taint"},
		{"none note", "  image: golang:1.23\n  run_args: [\"--network=none\"]\n",
			"; container network none (from container.run_args)", ""},
		{"host", "  image: golang:1.23\n  run_args: [\"--network=host\"]\n",
			"after taint: commands refused,", "commands in container"},
		{"host note", "  image: golang:1.23\n  run_args: [\"--net\", \"host\"]\n",
			`; container network host (from container.run_args): network mode "host" can't be disconnected`, ""},
		{"named network", "  image: golang:1.23\n  run_args: [\"--network\", \"ci-net\"]\n",
			"(network removed at taint), network denied,", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checkEnv(t)
			t.Setenv("ANTHROPIC_API_KEY", "k")
			cfg := writeLockdownConfig(t, "environment: container\ncontainer:\n"+c.container+"untrusted_content:\n  enabled: true\n")
			code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir(), "--config", cfg)
			if code != 0 {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			got := reportLine(t, stdout, "lockdown")
			if !strings.Contains(got, c.want) || (c.notWant != "" && strings.Contains(got, c.notWant)) {
				t.Errorf("lockdown line %q", got)
			}
		})
	}
}

func TestCheckLockdownContainerPodman(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake runtime is a shell script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cases := []struct {
		name, script, want string
	}{
		{"rootless", `[ "$1" = info ] && echo true`,
			`after taint: commands refused,`},
		{"rootless note", `[ "$1" = info ] && echo true`,
			`; container network slirp4netns/pasta (runtime default): network mode "slirp4netns/pasta" can't be disconnected`},
		{"rootful", `[ "$1" = info ] && echo false`,
			`(network removed at taint), network denied,`},
		{"info fails", `echo "no daemon" >&2; exit 1`,
			`; container network: not checked (podman info failed: no daemon); a run checks it when the container starts`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checkEnv(t)
			t.Setenv("ANTHROPIC_API_KEY", "k")
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "podman"), []byte("#!"+sh+"\n"+c.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cfg := writeLockdownConfig(t, "environment: container\ncontainer:\n  runtime: podman\n  image: golang:1.23\nuntrusted_content:\n  enabled: true\n")
			code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir(), "--config", cfg)
			if code != 0 {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			if got := reportLine(t, stdout, "lockdown"); !strings.Contains(got, c.want) {
				t.Errorf("lockdown line %q", got)
			}
		})
	}
}
