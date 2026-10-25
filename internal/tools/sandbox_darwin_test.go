//go:build darwin

package tools

import (
	"net"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

func TestSandboxProfileAllowlist(t *testing.T) {
	s := &Sandbox{Writable: []string{"/w"}}
	if p := s.profile(); !strings.Contains(p, "(deny network*)") || strings.Contains(p, "network-outbound") {
		t.Errorf("denied profile:\n%s", p)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.proxy = sandboxnet.NewProxy(nil, ln)
	defer s.proxy.Close()
	want := `(allow network-outbound (remote ip "localhost:` + strings.TrimPrefix(ln.Addr().String(), "127.0.0.1:") + `"))`
	if p := s.profile(); !strings.Contains(p, "(deny network*)\n"+want+"\n") {
		t.Errorf("allowlist profile:\n%s", p)
	}
}
