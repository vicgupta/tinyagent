package sandboxnet

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProxyNarrowRefusesNewHostsAndKeepsListed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	hosts := map[string]string{"keep.test": "127.0.0.1", "drop.test": "127.0.0.1"}
	p, pu := startProxy(t, []string{"keep.test:" + port, "drop.test:" + port, "127.0.0.1:" + port}, hosts)
	c := proxiedClient(pu)
	for _, h := range []string{"keep.test", "drop.test"} {
		if code, body := get(t, c, "http://"+h+":"+port+"/"); code != 200 || body != "ok" {
			t.Fatalf("%s before narrow: %d %q", h, code, body)
		}
	}
	if p.Narrowed() {
		t.Fatal("narrowed before Narrow")
	}
	// keep.test stays; other.test is in the lockdown list but not in the
	// proxy's own rules, so it stays refused for the original reason.
	rules, err := ParseRules([]string{"keep.test:" + port, "other.test"})
	if err != nil {
		t.Fatal(err)
	}
	p.Narrow(rules)
	if !p.Narrowed() {
		t.Fatal("Narrowed = false")
	}
	if code, body := get(t, c, "http://keep.test:"+port+"/"); code != 200 || body != "ok" {
		t.Errorf("keep.test after narrow: %d %q", code, body)
	}
	if code, body := get(t, c, "http://drop.test:"+port+"/"); code != http.StatusForbidden || !strings.Contains(body, ReasonLockdown) {
		t.Errorf("drop.test after narrow: %d %q", code, body)
	}
	if code, body := get(t, c, "http://other.test/"); code != http.StatusForbidden || !strings.Contains(body, ReasonNotAllowed) {
		t.Errorf("other.test after narrow: %d %q", code, body)
	}
	d := p.TakeDenials()
	if len(d) != 2 || d[0] != (Denial{Host: "drop.test", Port: srv.Listener.Addr().(*net.TCPAddr).Port, Reason: ReasonLockdown}) || d[1].Reason != ReasonNotAllowed {
		t.Errorf("denials: %+v", d)
	}
}

func TestProxyNarrowEmptyRefusesEverything(t *testing.T) {
	p, pu := startProxy(t, []string{"*.example.test"}, map[string]string{"a.example.test": "93.184.215.14"})
	p.Narrow(nil)
	if code, body := get(t, proxiedClient(pu), "http://a.example.test/"); code != http.StatusForbidden || !strings.Contains(body, ReasonLockdown) {
		t.Errorf("after empty narrow: %d %q", code, body)
	}
}

// TestProxyNarrowClosesOpenTunnels checks that a CONNECT tunnel opened
// before Narrow is closed by it, even to a host the narrowed rules allow.
func TestProxyNarrowClosesOpenTunnels(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }() // echo
		}
	}()
	addr := up.Addr().String()
	p, pu := startProxy(t, []string{addr}, nil)
	conn, err := net.Dial("tcp", pu.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "CONNECT "+addr+" HTTP/1.1\r\nHost: "+addr+"\r\n\r\n")
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, "200") {
		t.Fatalf("CONNECT: %q %v", line, err)
	}
	br.ReadString('\n') // blank line
	io.WriteString(conn, "ping\n")
	if got, _ := br.ReadString('\n'); got != "ping\n" {
		t.Fatalf("echo before narrow = %q", got)
	}
	rules, _ := ParseRules([]string{addr})
	p.Narrow(rules)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "pong\n")
	if got, err := br.ReadString('\n'); err == nil {
		t.Errorf("tunnel still open after narrow: read %q", got)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Error("tunnel not closed by Narrow (read timed out)")
	}
}
