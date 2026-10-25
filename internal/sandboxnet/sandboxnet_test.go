package sandboxnet

import (
	"context"
	"crypto/tls"
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

func TestParseRule(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Rule
	}{
		{"Proxy.Golang.org.", Rule{Host: "proxy.golang.org"}},
		{"*.npmjs.org", Rule{Host: "npmjs.org", Wildcard: true}},
		{"api.example.com:8443", Rule{Host: "api.example.com", Port: 8443}},
		{"127.0.0.1:9000", Rule{Addr: netip.MustParseAddr("127.0.0.1"), Port: 9000}},
		{"[::1]:9000", Rule{Addr: netip.MustParseAddr("::1"), Port: 9000}},
		{"::1", Rule{Addr: netip.MustParseAddr("::1")}},
		{"169.254.169.254", Rule{Addr: netip.MustParseAddr("169.254.169.254")}},
	} {
		got, err := ParseRule(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseRule(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "*", " * ", "*.", "foo.*.com", "a..b", "host:0", "host:99999", "host:x", "[::1", "[::1]x", "http://x", "*.1.2.3.4:80:"} {
		if r, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) = %+v, want error", bad, r)
		}
	}
	if _, err := ParseRules([]string{"ok.example", "*"}); err == nil || !strings.Contains(err.Error(), `"*"`) {
		t.Errorf("ParseRules error should name the entry: %v", err)
	}
}

func TestRuleAllows(t *testing.T) {
	rules, err := ParseRules([]string{"*.example.test", "exact.test", "ports.test:8080", "10.0.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{rules: rules}
	for _, tc := range []struct {
		host string
		port int
		want bool
	}{
		{"a.example.test", 443, true},
		{"b.a.example.test", 80, true},
		{"example.test", 443, false},
		{"badexample.test", 443, false},
		{"exact.test", 443, true},
		{"exact.test", 22, false},
		{"sub.exact.test", 443, false},
		{"ports.test", 8080, true},
		{"ports.test", 443, false},
		{"10.0.0.5", 80, true},
		{"10.0.0.6", 80, false},
		{"evil.example", 443, false},
	} {
		if got := p.allowed(normalizeHost(tc.host), tc.port); got != tc.want {
			t.Errorf("allowed(%s, %d) = %v, want %v", tc.host, tc.port, got, tc.want)
		}
	}
}

// startProxy runs a proxy for entries; hosts maps names to fake DNS results.
func startProxy(t *testing.T, entries []string, hosts map[string]string) (*Proxy, *url.URL) {
	t.Helper()
	rules, err := ParseRules(entries)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxy(rules, ln)
	p.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if a, ok := hosts[host]; ok {
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	go p.Serve()
	t.Cleanup(func() { p.Close() })
	return p, &url.URL{Scheme: "http", Host: ln.Addr().String()}
}

func proxiedClient(proxy *url.URL) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxy),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestProxyAllowsListedHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "plain "+r.URL.Path) }))
	defer srv.Close()
	tsrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls "+r.URL.Path) }))
	defer tsrv.Close()
	plainPort := srv.Listener.Addr().(*net.TCPAddr).Port
	tlsPort := tsrv.Listener.Addr().(*net.TCPAddr).Port
	// The fixtures listen on loopback, so the literal address must be listed.
	p, pu := startProxy(t, []string{"127.0.0.1:" + strconv.Itoa(plainPort), "127.0.0.1:" + strconv.Itoa(tlsPort)}, nil)
	c := proxiedClient(pu)

	if code, body := get(t, c, srv.URL+"/a"); code != 200 || body != "plain /a" {
		t.Errorf("plain HTTP: %d %q", code, body)
	}
	if code, body := get(t, c, tsrv.URL+"/b"); code != 200 || body != "tls /b" {
		t.Errorf("CONNECT: %d %q", code, body)
	}
	if d := p.TakeDenials(); len(d) != 0 {
		t.Errorf("unexpected denials: %v", d)
	}
}

func TestProxyDeniesUnlistedHosts(t *testing.T) {
	p, pu := startProxy(t, []string{"*.example.test"}, map[string]string{"a.example.test": "93.184.215.14"})
	c := proxiedClient(pu)
	for _, u := range []string{"http://evil.example/", "http://example.test/", "http://a.example.test:8080/"} {
		code, body := get(t, c, u)
		if code != http.StatusForbidden || !strings.Contains(body, ReasonNotAllowed) {
			t.Errorf("%s: %d %q", u, code, body)
		}
	}
	// CONNECT refusals surface as a proxy error in the client.
	if _, err := c.Get("https://evil.example/"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("CONNECT to unlisted host: %v", err)
	}
	d := p.TakeDenials()
	if len(d) != 4 || d[0] != (Denial{Host: "evil.example", Port: 80, Reason: ReasonNotAllowed}) || d[2].Port != 8080 || d[3].Port != 443 {
		t.Errorf("denials: %+v", d)
	}
	if len(p.TakeDenials()) != 0 {
		t.Error("TakeDenials did not reset")
	}
}

func TestProxyRefusesNonPublicAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	hosts := map[string]string{"local.test": "127.0.0.1", "meta.test": "169.254.169.254", "listed.test": "127.0.0.1"}
	p, pu := startProxy(t, []string{"local.test:" + port, "meta.test", "listed.test:" + port}, hosts)
	c := proxiedClient(pu)
	for _, u := range []string{"http://local.test:" + port + "/", "http://meta.test/"} {
		if code, body := get(t, c, u); code != http.StatusForbidden || !strings.Contains(body, ReasonNonPublic) {
			t.Errorf("%s: %d %q", u, code, body)
		}
	}
	if d := p.TakeDenials(); len(d) != 2 || d[1].Host != "meta.test" || !strings.Contains(d[1].Reason, "169.254.169.254") {
		t.Errorf("denials: %+v", d)
	}

	// With the literal address listed for that port, the name may resolve to it.
	_, pu2 := startProxy(t, []string{"listed.test:" + port, "127.0.0.1:" + port}, hosts)
	if code, body := get(t, proxiedClient(pu2), "http://listed.test:"+port+"/"); code != 200 || body != "ok" {
		t.Errorf("listed literal: %d %q", code, body)
	}
}
