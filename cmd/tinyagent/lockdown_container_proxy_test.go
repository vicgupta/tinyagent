package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
)

func containerProxyState(t *testing.T, on bool) *lockdown.State {
	t.Helper()
	st, _ := lockdown.New(nil)
	if err := st.SetCommandAllowedDomains([]string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	st.SetContainerProxy(on)
	return st
}

// Without container_proxy, allowed_domains still has no effect in the
// container, and lockdown says how to turn it on.
func TestConfineContainerLockdownProxyOff(t *testing.T) {
	c, _ := fakeLockdownContainer(t, "bridge|bridge ")
	st := containerProxyState(t, false)
	var buf bytes.Buffer
	confineLockdownEnv(st, c, &buf)
	if !strings.Contains(buf.String(), "unless untrusted_content.lockdown.container_proxy is true") {
		t.Errorf("stderr %q", buf.String())
	}
	if c.LockdownProxyNetwork() != "" || !strings.Contains(st.Describe(), "network denied") {
		t.Errorf("proxy %q, describe %q", c.LockdownProxyNetwork(), st.Describe())
	}
}

// When the proxy can't be set up, commands keep running with no network
// after taint, and lockdown says why.
func TestConfineContainerLockdownProxyUnavailable(t *testing.T) {
	c, _ := fakeLockdownContainer(t, "bridge|bridge ")
	st := containerProxyState(t, true)
	var buf bytes.Buffer
	confineLockdownEnv(st, c, &buf)
	if !strings.Contains(buf.String(), "container_proxy not available, so container commands have no network after taint") {
		t.Errorf("stderr %q", buf.String())
	}
	if !st.CommandsConfined() || !strings.Contains(st.Describe(), "commands in container (network removed at taint), network denied") {
		t.Errorf("confined %v, describe %q", st.CommandsConfined(), st.Describe())
	}
}

// --check reports the proxied policy when container_proxy is on.
func TestCheckLockdownContainerProxy(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	cfg := writeLockdownConfig(t, "environment: container\ncontainer:\n  image: golang:1.23\n  run_args: [--network, bridge]\nuntrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domains: [proxy.golang.org]\n    container_proxy: true\n")
	code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir(), "--config", cfg)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	got := reportLine(t, stdout, "lockdown")
	if !strings.Contains(got, "commands in container (network removed at taint), network → 1 domain (proxy.golang.org)") || !strings.Contains(got, "container_proxy: an internal network and proxy are set up when the container starts") {
		t.Errorf("lockdown line %q", got)
	}
}

// container_proxy without allowed_domains exits 2.
func TestCheckLockdownContainerProxyNeedsDomains(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	cfg := writeLockdownConfig(t, "environment: container\ncontainer:\n  image: golang:1.23\nuntrusted_content:\n  enabled: true\n  lockdown:\n    container_proxy: true\n")
	code, _, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir(), "--config", cfg)
	if code != 2 || !strings.Contains(stderr, "container_proxy needs untrusted_content.lockdown.allowed_domains") {
		t.Errorf("exit %d; stderr:\n%s", code, stderr)
	}
}
