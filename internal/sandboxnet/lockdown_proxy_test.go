package sandboxnet

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestLockdownProxyAllowsOnlyItsRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	rules, err := ParseRules([]string{"keep.test:" + port, "127.0.0.1:" + port})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := NewLockdownProxy(rules, ln)
	p.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	go p.Serve()
	defer p.Close()
	if !p.Narrowed() {
		t.Error("lockdown proxy not narrowed")
	}
	c := proxiedClient(&url.URL{Scheme: "http", Host: ln.Addr().String()})
	if code, body := get(t, c, "http://keep.test:"+port+"/"); code != 200 || body != "ok" {
		t.Errorf("keep.test: %d %q", code, body)
	}
	if code, body := get(t, c, "http://drop.test:"+port+"/"); code != http.StatusForbidden || !strings.Contains(body, ReasonLockdown) {
		t.Errorf("drop.test: %d %q", code, body)
	}
	if d := p.TakeDenials(); len(d) != 1 || d[0].Host != "drop.test" || d[0].Reason != ReasonLockdown {
		t.Errorf("denials: %+v", d)
	}
}

func TestProxyNotAllowedReasonDefault(t *testing.T) {
	if r := (&Proxy{}).reasonNotAllowed(); r != ReasonNotAllowed {
		t.Errorf("reason %q", r)
	}
}
