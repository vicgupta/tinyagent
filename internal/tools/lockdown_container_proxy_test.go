package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeProxyRuntime stands in for docker for the container lockdown proxy.
// State lives in $FAKE_NET: mode, networks (one per line), gateway (what
// `network inspect` prints), log. `network connect` fails when
// $FAKE_NET/noconnect exists and `network create` when $FAKE_NET/nocreate
// exists. `exec` prints its -e variables to $FAKE_NET/env.
const fakeProxyRuntime = `#!/bin/bash
echo "$*" >> "$FAKE_NET/log"
case "$1" in
inspect)
  printf '%s|' "$(cat "$FAKE_NET/mode")"
  while read -r n; do [ -n "$n" ] && printf '%s ' "$n"; done < "$FAKE_NET/networks"
  echo ;;
network)
  case "$2" in
  create) [ -e "$FAKE_NET/nocreate" ] && { echo "permission denied" >&2; exit 1; }; echo created ;;
  inspect) cat "$FAKE_NET/gateway" ;;
  connect) [ -e "$FAKE_NET/noconnect" ] && { echo "no such network" >&2; exit 1; }; echo "$3" >> "$FAKE_NET/networks" ;;
  disconnect) grep -vx "$3" "$FAKE_NET/networks" > "$FAKE_NET/networks.new"; mv "$FAKE_NET/networks.new" "$FAKE_NET/networks" ;;
  rm) ;;
  esac ;;
rm) ;;
exec)
  shift; : > "$FAKE_NET/env"
  while [ $# -gt 0 ]; do case "$1" in -w) shift 2 ;; -e) echo "$2" >> "$FAKE_NET/env"; shift 2 ;; *) break ;; esac; done
  shift; exec "$@" ;;
esac
`

func fakeProxyContainer(t *testing.T, gateway string) (*Container, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake runtime is a bash script")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakedocker")
	if err := os.WriteFile(bin, []byte(fakeProxyRuntime), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{"mode": "bridge", "networks": "bridge\n", "gateway": gateway + "\n", "log": ""} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKE_NET", state)
	// The fake network's gateway isn't a host address: listen on loopback.
	old := listenGateway
	listenGateway = func(netip.Addr) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	t.Cleanup(func() { listenGateway = old })
	return &Container{Runtime: bin, ID: "fakeid", Mount: dir}, state
}

func readState(t *testing.T, state, name string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(state, name))
	return string(b)
}

func proxyURLFrom(env string) string {
	for _, l := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(l, "HTTPS_PROXY="); ok {
			return v
		}
	}
	return ""
}

func TestContainerLockdownProxy(t *testing.T) {
	c, state := fakeProxyContainer(t, "172.30.0.1 fd00::1")
	ctx := context.Background()
	if err := c.PrepareLockdownProxy(ctx, []string{"proxy.golang.org"}); err == nil {
		t.Fatal("proxy prepared before the network switch")
	}
	if _, err := c.PrepareLockdownNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareLockdownProxy(ctx, []string{"proxy.golang.org"}); err != nil {
		t.Fatalf("prepare proxy: %v", err)
	}
	if err := c.PrepareLockdownProxy(ctx, []string{"proxy.golang.org"}); err == nil {
		t.Error("prepared twice")
	}
	name := c.LockdownProxyNetwork()
	if !strings.HasPrefix(name, "tinyagent-lockdown-") {
		t.Fatalf("network %q", name)
	}
	if log := readState(t, state, "log"); !strings.Contains(log, "network create --internal --label tinyagent=1 "+name) {
		t.Errorf("log %q", log)
	}
	// Before taint commands get no proxy.
	if err := runInContainer(c, state); err != nil {
		t.Fatal(err)
	}
	if env := readState(t, state, "env"); strings.Contains(env, "PROXY") {
		t.Errorf("proxy before taint: %q", env)
	}
	if err := c.RemoveLockdownNetwork(ctx); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, nets, _ := c.inspectNetworks(ctx); len(nets) != 1 || nets[0] != name {
		t.Fatalf("networks after taint %v", nets)
	}
	if err := c.LockdownProxyErr(); err != nil {
		t.Fatal(err)
	}
	if err := runInContainer(c, state); err != nil {
		t.Fatal(err)
	}
	env := readState(t, state, "env")
	url := proxyURLFrom(env)
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(env, "NO_PROXY=\n") {
		t.Fatalf("env after taint %q", env)
	}
	// The proxy refuses hosts outside allowed_domains and records it.
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(url, "http://"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	conn.Close()
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	if d := c.TakeNetworkDenials(); len(d) != 1 || d[0].Host != "evil.example" || !strings.Contains(d[0].Reason, "untrusted_content.lockdown.allowed_domains") {
		t.Errorf("denials %+v", d)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if log := readState(t, state, "log"); !strings.Contains(log, "network rm "+name) {
		t.Errorf("network not removed: %q", log)
	}
	if _, err := net.DialTimeout("tcp", strings.TrimPrefix(url, "http://"), time.Second); err == nil {
		t.Error("proxy still listening after Close")
	}
}

// A failed connect leaves the container with no network: commands run
// without the proxy.
func TestContainerLockdownProxyConnectFails(t *testing.T) {
	c, state := fakeProxyContainer(t, "172.30.0.1")
	ctx := context.Background()
	if _, err := c.PrepareLockdownNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareLockdownProxy(ctx, []string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "noconnect"), nil, 0o644)
	if err := c.RemoveLockdownNetwork(ctx); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := c.LockdownProxyErr(); err == nil || !strings.Contains(err.Error(), "no such network") {
		t.Fatalf("proxy err %v", err)
	}
	if _, nets, _ := c.inspectNetworks(ctx); len(nets) != 0 {
		t.Fatalf("networks %v", nets)
	}
	if err := runInContainer(c, state); err != nil {
		t.Fatal(err)
	}
	if env := readState(t, state, "env"); strings.Contains(env, "PROXY") {
		t.Errorf("proxy env without the network: %q", env)
	}
	c.Close()
}

func TestContainerLockdownProxyPrepareErrors(t *testing.T) {
	cases := []struct {
		name, gateway, marker, want string
		listen                      bool
		domains                     []string
	}{
		{name: "no domains", gateway: "172.30.0.1", want: "allowed_domains is empty"},
		{name: "bad domain", gateway: "172.30.0.1", domains: []string{"*"}, want: "allowed_domains"},
		{name: "create fails", gateway: "172.30.0.1", marker: "nocreate", domains: []string{"a.example"}, want: "permission denied"},
		{name: "no ipv4 gateway", gateway: "fd00::1", domains: []string{"a.example"}, want: "no IPv4 gateway"},
		{name: "listen fails", gateway: "172.30.0.1", listen: true, domains: []string{"a.example"}, want: "can't listen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, state := fakeProxyContainer(t, tc.gateway)
			if tc.listen {
				listenGateway = func(netip.Addr) (net.Listener, error) { return nil, errors.New("cannot assign requested address") }
			}
			if tc.marker != "" {
				os.WriteFile(filepath.Join(state, tc.marker), nil, 0o644)
			}
			ctx := context.Background()
			if _, err := c.PrepareLockdownNetwork(ctx); err != nil {
				t.Fatal(err)
			}
			err := c.PrepareLockdownProxy(ctx, tc.domains)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
			if c.LockdownProxyNetwork() != "" {
				t.Error("proxy kept after an error")
			}
			log := readState(t, state, "log")
			if strings.Contains(log, "network create") && tc.marker == "" && !strings.Contains(log, "network rm tinyagent-lockdown-") {
				t.Errorf("created network not removed: %q", log)
			}
			// The plain switch still works: no network after taint.
			if err := c.RemoveLockdownNetwork(ctx); err != nil {
				t.Fatal(err)
			}
			if _, nets, _ := c.inspectNetworks(ctx); len(nets) != 0 {
				t.Errorf("networks %v", nets)
			}
		})
	}
}

func TestGatewayFormatAndParse(t *testing.T) {
	if f := gatewayFormat("/usr/bin/podman"); !strings.Contains(f, ".Subnets") {
		t.Errorf("podman format %q", f)
	}
	if f := gatewayFormat("docker"); !strings.Contains(f, ".IPAM.Config") {
		t.Errorf("docker format %q", f)
	}
	for in, want := range map[string]string{"fd00::1 10.89.0.1 ": "10.89.0.1", "172.18.0.1": "172.18.0.1", "": "", "0.0.0.0 <no value>": ""} {
		got, ok := ipv4Gateway(in)
		if (want == "") == ok || (ok && got.String() != want) {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
}
