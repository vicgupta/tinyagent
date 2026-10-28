package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeNetRuntime stands in for docker for the lockdown network switch. The
// network mode is in $FAKE_NET/mode and the connected networks are one per
// line in $FAKE_NET/networks. `network disconnect` removes a line unless
// $FAKE_NET/fail exists (it then fails) or $FAKE_NET/stuck exists (it then
// succeeds without disconnecting). Every call is logged to $FAKE_NET/log.
const fakeNetRuntime = `#!/bin/bash
echo "$*" >> "$FAKE_NET/log"
case "$1" in
inspect)
  printf '%s|' "$(cat "$FAKE_NET/mode")"
  while read -r n; do [ -n "$n" ] && printf '%s ' "$n"; done < "$FAKE_NET/networks"
  echo ;;
network)
  [ -e "$FAKE_NET/fail" ] && { echo "no such network" >&2; exit 1; }
  [ -e "$FAKE_NET/stuck" ] && exit 0
  grep -vx "$3" "$FAKE_NET/networks" > "$FAKE_NET/networks.new"; mv "$FAKE_NET/networks.new" "$FAKE_NET/networks" ;;
exec)
  shift; while [ $# -gt 0 ]; do case "$1" in -w|-e) shift 2 ;; *) break ;; esac; done
  shift; exec "$@" ;;
esac
`

func fakeNetContainer(t *testing.T, mode string, networks ...string) (*Container, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake runtime is a bash script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakedocker")
	if err := os.WriteFile(bin, []byte(fakeNetRuntime), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(state, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mode", mode)
	write("networks", strings.Join(networks, "\n")+"\n")
	write("log", "")
	t.Setenv("FAKE_NET", state)
	return &Container{Runtime: bin, ID: "fakeid", Mount: dir}, state
}

func runInContainer(c *Container, root string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.Command(ctx, root, "true").Run()
}

func TestContainerLockdownRemovesNetwork(t *testing.T) {
	c, state := fakeNetContainer(t, "bridge", "bridge", "devnet")
	ctx := context.Background()
	if err := c.RemoveLockdownNetwork(ctx); err == nil {
		t.Fatal("removed the network without preparing")
	}
	denied, err := c.PrepareLockdownNetwork(ctx)
	if err != nil || denied || !c.LockdownNetworkPrepared() {
		t.Fatalf("prepare: denied %v, err %v", denied, err)
	}
	if err := runInContainer(c, state); err != nil {
		t.Fatalf("command before taint: %v", err)
	}
	if err := c.RemoveLockdownNetwork(ctx); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, left, err := c.inspectNetworks(ctx); err != nil || len(left) != 0 {
		t.Fatalf("networks left %v, err %v", left, err)
	}
	log, _ := os.ReadFile(filepath.Join(state, "log"))
	for _, want := range []string{"network disconnect bridge fakeid", "network disconnect devnet fakeid"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log %q lacks %q", log, want)
		}
	}
	if err := runInContainer(c, state); err != nil {
		t.Fatalf("command after taint: %v", err)
	}
	// A second call is a no-op.
	os.WriteFile(filepath.Join(state, "fail"), nil, 0o644)
	if err := c.RemoveLockdownNetwork(ctx); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	// Copies (workers) share the switch.
	cp := *c
	if !cp.LockdownNetworkPrepared() {
		t.Error("copy lost the switch")
	}
}

func TestContainerLockdownNoNetwork(t *testing.T) {
	c, _ := fakeNetContainer(t, "none", "none")
	denied, err := c.PrepareLockdownNetwork(context.Background())
	if err != nil || !denied || c.LockdownNetworkPrepared() {
		t.Fatalf("denied %v, err %v, prepared %v", denied, err, c.LockdownNetworkPrepared())
	}
}

func TestContainerLockdownUnsupportedModes(t *testing.T) {
	for _, mode := range []string{"host", "container:abc", "ns:/proc/1/ns/net", "slirp4netns", "pasta", "bridge"} {
		t.Run(mode, func(t *testing.T) {
			networks := []string{"host"}
			if mode != "host" {
				networks = nil
			}
			c, _ := fakeNetContainer(t, mode, networks...)
			if _, err := c.PrepareLockdownNetwork(context.Background()); err == nil || !strings.Contains(err.Error(), mode) {
				t.Fatalf("err %v", err)
			}
			if c.LockdownNetworkPrepared() {
				t.Error("prepared anyway")
			}
		})
	}
}

func TestContainerLockdownFailsClosed(t *testing.T) {
	for _, marker := range []string{"fail", "stuck"} {
		t.Run(marker, func(t *testing.T) {
			c, state := fakeNetContainer(t, "bridge", "bridge")
			ctx := context.Background()
			if _, err := c.PrepareLockdownNetwork(ctx); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(state, marker), nil, 0o644)
			err := c.RemoveLockdownNetwork(ctx)
			if err == nil || !strings.Contains(err.Error(), "still connected to bridge") {
				t.Fatalf("remove err %v", err)
			}
			if marker == "fail" && !strings.Contains(err.Error(), "no such network") {
				t.Errorf("err %v lacks the runtime's message", err)
			}
			err = runInContainer(c, state)
			if err == nil || !strings.Contains(err.Error(), "lockdown: commands refused") {
				t.Fatalf("command after failed removal: %v", err)
			}
			if err := c.RemoveLockdownNetwork(ctx); err == nil {
				t.Error("second remove forgot the failure")
			}
		})
	}
}

func TestContainerLockdownInspectError(t *testing.T) {
	c, _ := fakeNetContainer(t, "bridge", "bridge")
	c.Runtime = filepath.Join(t.TempDir(), "missing")
	if _, err := c.PrepareLockdownNetwork(context.Background()); err == nil || !strings.Contains(err.Error(), "inspect failed") {
		t.Fatalf("err %v", err)
	}
	var nilC *Container
	if _, err := nilC.PrepareLockdownNetwork(context.Background()); err == nil {
		t.Error("nil container prepared")
	}
}
