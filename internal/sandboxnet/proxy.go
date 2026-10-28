package sandboxnet

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// Denial is one request the proxy refused.
type Denial struct {
	Host   string
	Port   int
	Reason string
}

// Denial reasons.
const (
	ReasonNotAllowed = "host not in sandbox_allowed_domains"
	ReasonNonPublic  = "host resolves to a loopback, link-local or private address"
)

type deniedError struct{ reason string }

func (e *deniedError) Error() string { return e.reason }

// Proxy is an HTTP proxy (CONNECT for HTTPS, absolute-URI requests for plain
// HTTP) that forwards only to hosts its rules allow. It dials directly and
// never chains to an upstream proxy.
type Proxy struct {
	rules []Rule
	ln    net.Listener
	srv   *http.Server

	// Resolve and Dial default to the system resolver and a net.Dialer;
	// tests replace them.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)

	mu      sync.Mutex
	denials []Denial
	// narrow is the untrusted-content lockdown restriction (see Narrow),
	// guarded by mu.
	narrow narrowState
}

// NewProxy returns a proxy for rules that serves on ln once Serve is called.
func NewProxy(rules []Rule, ln net.Listener) *Proxy {
	p := &Proxy{rules: rules, ln: ln}
	p.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	p.Dial = d.DialContext
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	return p
}

// Serve serves until Close.
func (p *Proxy) Serve() {
	p.srv.Serve(p.ln)
}

// Addr is the listener's address.
func (p *Proxy) Addr() net.Addr { return p.ln.Addr() }

// Close stops the listener and every open connection.
func (p *Proxy) Close() error { return p.srv.Close() }

// TakeDenials returns the requests refused since the last call.
func (p *Proxy) TakeDenials() []Denial {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.denials
	p.denials = nil
	return d
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "tinyagent sandbox proxy: only CONNECT and absolute http:// requests are supported", http.StatusBadRequest)
		return
	}
	host, port, err := splitHostPort(r.URL.Host, 80)
	if err != nil {
		http.Error(w, "tinyagent sandbox proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	conn, err := p.connect(r.Context(), host, port)
	if err != nil {
		p.fail(w, host, port, err)
		return
	}
	defer conn.Close()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Close = true
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive"} {
		out.Header.Del(h)
	}
	if err := out.Write(conn); err != nil {
		http.Error(w, "tinyagent sandbox proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), out)
	if err != nil {
		http.Error(w, "tinyagent sandbox proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if k != "Connection" && k != "Keep-Alive" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *Proxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitHostPort(r.Host, 443)
	if err != nil {
		http.Error(w, "tinyagent sandbox proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	upstream, err := p.connect(r.Context(), host, port)
	if err != nil {
		p.fail(w, host, port, err)
		return
	}
	defer upstream.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tinyagent sandbox proxy: hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(upstream, buf) // buf holds any bytes read past the request
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, upstream)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}

// connect checks host and port against the rules, resolves host and dials
// the first address that is public or listed literally.
func (p *Proxy) connect(ctx context.Context, host string, port int) (net.Conn, error) {
	host = normalizeHost(host)
	if !p.allowed(host, port) {
		return nil, &deniedError{ReasonNotAllowed}
	}
	if !p.narrowAllows(host, port) {
		return nil, &deniedError{ReasonLockdown}
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a} // listed literally, since it matched
	} else {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		all, err := p.Resolve(rctx, host)
		cancel()
		if err != nil {
			return nil, err
		}
		for _, a := range all {
			if !isNonPublic(a) || p.allowed(a.Unmap().String(), port) {
				addrs = append(addrs, a)
			}
		}
		if len(addrs) == 0 && len(all) > 0 {
			return nil, &deniedError{fmt.Sprintf("%s (%s)", ReasonNonPublic, all[0].Unmap())}
		}
	}
	var lastErr error = fmt.Errorf("no addresses for %s", host)
	for _, a := range addrs {
		c, err := p.Dial(ctx, "tcp", net.JoinHostPort(a.Unmap().String(), strconv.Itoa(port)))
		if err == nil {
			return p.track(c, host, port)
		}
		lastErr = err
	}
	return nil, lastErr
}

func (p *Proxy) allowed(host string, port int) bool {
	for _, r := range p.rules {
		if r.Allows(host, port) {
			return true
		}
	}
	return false
}

// fail answers a refused request with 403 and records it, or reports any
// other error as 502.
func (p *Proxy) fail(w http.ResponseWriter, host string, port int, err error) {
	var d *deniedError
	if !errors.As(err, &d) {
		http.Error(w, fmt.Sprintf("tinyagent sandbox proxy: %s:%d: %v", host, port, err), http.StatusBadGateway)
		return
	}
	host = normalizeHost(host)
	p.mu.Lock()
	p.denials = append(p.denials, Denial{Host: host, Port: port, Reason: d.reason})
	p.mu.Unlock()
	w.Header().Set("X-Proxy-Error", "blocked-by-allowlist")
	http.Error(w, fmt.Sprintf("tinyagent sandbox: %s: %s (port %d)", d.reason, host, port), http.StatusForbidden)
}

func splitHostPort(hostport string, defaultPort int) (string, int, error) {
	host, ps, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port: a bare host or a bracketed IPv6 literal.
		return hostport, defaultPort, nil
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in %q", hostport)
	}
	return host, port, nil
}
