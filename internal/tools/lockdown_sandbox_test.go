package tools

import (
	"net"
	"testing"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

func TestCommandSandbox(t *testing.T) {
	sb := &Sandbox{}
	if CommandSandbox(Local{Sandbox: sb}) != sb || CommandSandbox(Local{}) != nil || CommandSandbox(&Container{}) != nil || CommandSandbox(nil) != nil {
		t.Error("CommandSandbox picked the wrong sandbox")
	}
}

func TestSandboxLockdownConfinable(t *testing.T) {
	var nilSB *Sandbox
	if nilSB.LockdownConfinable() || (&Sandbox{Network: true}).LockdownConfinable() || !(&Sandbox{}).LockdownConfinable() {
		t.Error("LockdownConfinable wrong")
	}
	if err := (&Sandbox{Network: true}).NarrowNetwork(nil); err == nil {
		t.Error("narrowed a sandbox_network sandbox")
	}
	if err := (&Sandbox{}).NarrowNetwork([]string{"*"}); err == nil {
		t.Error("accepted a bare *")
	}
	if err := (&Sandbox{}).NarrowNetwork(nil); err != nil {
		t.Errorf("network-denied sandbox: %v", err)
	}
}

func TestSandboxNarrowNetworkNarrowsProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := sandboxnet.ParseRules([]string{"proxy.golang.org"})
	p := sandboxnet.NewProxy(rules, ln)
	defer p.Close()
	s := &Sandbox{AllowedDomains: []string{"proxy.golang.org"}, proxy: p}
	if !s.Proxied() {
		t.Fatal("Proxied = false")
	}
	if err := s.NarrowNetwork([]string{"proxy.golang.org"}); err != nil || !p.Narrowed() {
		t.Errorf("NarrowNetwork: %v, narrowed %v", err, p.Narrowed())
	}
}
