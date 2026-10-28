package tools

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// fakeLockdownProbe replaces the host probe for one test.
func fakeLockdownProbe(t *testing.T, err error) {
	t.Helper()
	old := probeLockdownNetwork
	probeLockdownNetwork = func(bool) error { return err }
	t.Cleanup(func() { probeLockdownNetwork = old })
}

func TestPrepareLockdownNetworkDenied(t *testing.T) {
	fakeLockdownProbe(t, nil)
	sb := &Sandbox{Network: true}
	if sb.LockdownConfinable() || sb.DirectUntilTaint() {
		t.Fatal("sandbox_network confinable before PrepareLockdownNetwork")
	}
	if err := sb.PrepareLockdownNetwork(nil); err != nil {
		t.Fatal(err)
	}
	if !sb.LockdownConfinable() || !sb.DirectUntilTaint() || sb.Proxied() {
		t.Fatalf("after prepare: confinable %v direct %v proxied %v", sb.LockdownConfinable(), sb.DirectUntilTaint(), sb.Proxied())
	}
	if network, _, _ := sb.netState(); !network {
		t.Error("direct network taken away before taint")
	}
	worker := *sb // workers copy the sandbox
	if err := sb.NarrowNetwork(nil); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*Sandbox{"sandbox": sb, "worker copy": &worker} {
		if network, proxy, env := s.netState(); network || proxy != nil || env != nil {
			t.Errorf("%s after taint: network %v proxy %v env %v", name, network, proxy, env)
		}
	}
	if err := sb.NarrowNetwork(nil); err != nil {
		t.Errorf("second narrow: %v", err)
	}
	if err := sb.PrepareLockdownNetwork(nil); err == nil {
		t.Error("prepared twice")
	}
}

func TestPrepareLockdownNetworkProxied(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no sandbox proxy on this platform")
	}
	fakeLockdownProbe(t, nil)
	sb := &Sandbox{Network: true}
	defer sb.Cleanup()
	if err := sb.PrepareLockdownNetwork([]string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	if !sb.Proxied() {
		t.Error("Proxied = false")
	}
	if network, proxy, _ := sb.netState(); !network || proxy != nil {
		t.Error("switched before taint")
	}
	if err := sb.NarrowNetwork([]string{"proxy.golang.org"}); err != nil {
		t.Fatal(err)
	}
	network, proxy, env := sb.netState()
	if network || proxy == nil || !proxy.Narrowed() {
		t.Fatalf("after taint: network %v proxy %v", network, proxy)
	}
	if !strings.Contains(strings.Join(env, " "), "HTTPS_PROXY=") {
		t.Errorf("proxy env = %v", env)
	}
	if sb.TakeNetworkDenials() != nil {
		t.Error("unexpected denials")
	}
}

func TestPrepareLockdownNetworkFails(t *testing.T) {
	if err := (&Sandbox{}).PrepareLockdownNetwork(nil); err == nil {
		t.Error("prepared a sandbox without direct network")
	}
	var nilSB *Sandbox
	if err := nilSB.PrepareLockdownNetwork(nil); err == nil || nilSB.DirectUntilTaint() {
		t.Error("nil sandbox")
	}
	fakeLockdownProbe(t, nil)
	if err := (&Sandbox{Network: true}).PrepareLockdownNetwork([]string{"*"}); err == nil || !strings.Contains(err.Error(), "untrusted_content.lockdown.allowed_domains") {
		t.Errorf("bare *: %v", err)
	}
	fakeLockdownProbe(t, errors.New("sandbox: Landlock ABI 3 cannot deny network"))
	sb := &Sandbox{Network: true}
	if err := sb.PrepareLockdownNetwork(nil); err == nil || !strings.Contains(err.Error(), "cannot deny network") {
		t.Errorf("probe failure: %v", err)
	}
	if sb.LockdownConfinable() || sb.DirectUntilTaint() {
		t.Error("confinable after a failed prepare")
	}
	if err := sb.NarrowNetwork(nil); err == nil {
		t.Error("narrowed an unprepared sandbox_network sandbox")
	}
}
