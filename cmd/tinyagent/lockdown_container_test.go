package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// fakeLockdownRuntime stands in for docker: inspect prints
// $FAKE_NET/inspect, and `network disconnect` empties the network list
// unless $FAKE_NET/fail exists.
const fakeLockdownRuntime = `#!/bin/bash
case "$1" in
inspect) cat "$FAKE_NET/inspect" ;;
network)
  [ -e "$FAKE_NET/fail" ] && { echo "permission denied" >&2; exit 1; }
  echo "bridge|" > "$FAKE_NET/inspect" ;;
esac
`

func fakeLockdownContainer(t *testing.T, inspect string) (*tools.Container, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake runtime is a bash script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakedocker")
	if err := os.WriteFile(bin, []byte(fakeLockdownRuntime), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inspect"), []byte(inspect+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_NET", dir)
	return &tools.Container{Runtime: bin, ID: "fakeid", Mount: dir}, dir
}

func TestConfineContainerLockdownCommands(t *testing.T) {
	c, _ := fakeLockdownContainer(t, "bridge|bridge ")
	st, _ := lockdown.New(nil)
	var buf bytes.Buffer
	confineLockdownEnv(st, c, &buf)
	if !st.CommandsConfined() || st.StopsProcessesAtTaint() || !c.LockdownNetworkPrepared() {
		t.Fatalf("confined %v, stops %v, prepared %v; stderr %q", st.CommandsConfined(), st.StopsProcessesAtTaint(), c.LockdownNetworkPrepared(), buf.String())
	}
	if !strings.Contains(buf.String(), "commands keep the container's network until taint; then it is disconnected") {
		t.Errorf("stderr %q", buf.String())
	}
	if !strings.Contains(st.Describe(), "commands in container (network removed at taint), network denied") {
		t.Errorf("describe %q", st.Describe())
	}
	st.Taint(lockdown.Source{Source: lockdown.SourceUntrustedTask})
	if !st.CommandsConfined() || strings.Contains(buf.String(), "error removing") {
		t.Errorf("taint: confined %v; stderr %q", st.CommandsConfined(), buf.String())
	}
}

func TestConfineContainerLockdownFailsClosedAtTaint(t *testing.T) {
	c, dir := fakeLockdownContainer(t, "bridge|bridge ")
	st, _ := lockdown.New(nil)
	var buf bytes.Buffer
	confineLockdownEnv(st, c, &buf)
	os.WriteFile(filepath.Join(dir, "fail"), nil, 0o644)
	st.Taint(lockdown.Source{Source: lockdown.SourceUntrustedTask})
	if st.CommandsConfined() || !st.StopsProcessesAtTaint() {
		t.Error("commands still confined after the network couldn't be removed")
	}
	if !strings.Contains(buf.String(), "error removing the container's network; commands are refused for the rest of the run") {
		t.Errorf("stderr %q", buf.String())
	}
}

func TestConfineContainerLockdownAlreadyDenied(t *testing.T) {
	c, _ := fakeLockdownContainer(t, "none|none ")
	st, _ := lockdown.New(nil)
	if err := st.SetCommandAllowedDomains([]string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	confineLockdownEnv(st, c, &buf)
	if !st.CommandsConfined() || c.LockdownNetworkPrepared() {
		t.Fatalf("confined %v, prepared %v", st.CommandsConfined(), c.LockdownNetworkPrepared())
	}
	if !strings.Contains(st.Describe(), "commands in container, network denied") {
		t.Errorf("describe %q", st.Describe())
	}
	if !strings.Contains(buf.String(), "allowed_domains has no effect in the container environment") {
		t.Errorf("stderr %q", buf.String())
	}
}

func TestConfineContainerLockdownUnsupported(t *testing.T) {
	c, _ := fakeLockdownContainer(t, "host|host ")
	st, _ := lockdown.New(nil)
	var buf bytes.Buffer
	confineLockdownEnv(st, c, &buf)
	if st.CommandsConfined() {
		t.Error("host-network container confined")
	}
	if !strings.Contains(buf.String(), "commands will be refused after taint: the container's network can't be removed at taint") || !strings.Contains(buf.String(), `"host"`) {
		t.Errorf("stderr %q", buf.String())
	}
}

// A local environment still goes through the sandbox path.
func TestConfineLockdownEnvLocal(t *testing.T) {
	st, _ := lockdown.New(nil)
	var buf bytes.Buffer
	confineLockdownEnv(st, tools.Local{}, &buf)
	if st.CommandsConfined() || !strings.Contains(buf.String(), "commands will be refused after taint") {
		t.Errorf("confined %v; stderr %q", st.CommandsConfined(), buf.String())
	}
}

// --check reports that container commands keep running after taint.
func TestCheckLockdownContainerCommands(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	cfg := writeLockdownConfig(t, "environment: container\ncontainer:\n  image: golang:1.23\nuntrusted_content:\n  enabled: true\n")
	code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir(), "--config", cfg)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if got := reportLine(t, stdout, "lockdown"); !strings.Contains(got, "after taint: commands in container (network removed at taint), network denied,") {
		t.Errorf("lockdown line %q", got)
	}
}
