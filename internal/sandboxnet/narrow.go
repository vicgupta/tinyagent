package sandboxnet

import (
	"net"
	"sync"
)

// ReasonLockdown is the denial reason for a host the proxy's rules allow
// but the untrusted-content lockdown allowlist doesn't (issue #171).
const ReasonLockdown = "host not in untrusted_content.lockdown.allowed_domains (the run has read untrusted content)"

// narrowState is the after-taint restriction of a proxy: once on, a host
// must match both the proxy's rules and these rules.
type narrowState struct {
	on    bool
	rules []Rule
	conns map[*trackedConn]struct{}
}

// Narrow restricts the proxy for the rest of its life to hosts that match
// both its own rules and rules; an empty rules refuses everything. Every
// upstream connection opened before the call is closed, so a tunnel opened
// before the restriction can't outlive it. A connection that is being
// dialed while Narrow runs is checked again against the narrowed rules
// before it is used. Narrowing again replaces rules but never widens past
// the proxy's own rules.
func (p *Proxy) Narrow(rules []Rule) {
	p.mu.Lock()
	p.narrow.on = true
	p.narrow.rules = append([]Rule(nil), rules...)
	open := make([]*trackedConn, 0, len(p.narrow.conns))
	for c := range p.narrow.conns {
		open = append(open, c)
	}
	p.narrow.conns = nil
	p.mu.Unlock()
	for _, c := range open {
		c.Conn.Close()
	}
}

// Narrowed reports whether Narrow has been called.
func (p *Proxy) Narrowed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.narrow.on
}

func (p *Proxy) narrowAllows(host string, port int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.narrowAllowsLocked(host, port)
}

func (p *Proxy) narrowAllowsLocked(host string, port int) bool {
	if !p.narrow.on {
		return true
	}
	for _, r := range p.narrow.rules {
		if r.Allows(host, port) {
			return true
		}
	}
	return false
}

// track registers an upstream connection so Narrow can close it. A
// connection whose host the narrowed rules no longer allow (Narrow ran
// while it was being dialed) is closed and refused instead.
func (p *Proxy) track(c net.Conn, host string, port int) (net.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.narrowAllowsLocked(host, port) {
		c.Close()
		return nil, &deniedError{ReasonLockdown}
	}
	if p.narrow.conns == nil {
		p.narrow.conns = map[*trackedConn]struct{}{}
	}
	t := &trackedConn{Conn: c, p: p}
	p.narrow.conns[t] = struct{}{}
	return t, nil
}

// trackedConn is an upstream connection that leaves the proxy's tracked
// set when closed.
type trackedConn struct {
	net.Conn
	p    *Proxy
	once sync.Once
}

func (t *trackedConn) Close() error {
	t.once.Do(func() {
		t.p.mu.Lock()
		delete(t.p.narrow.conns, t)
		t.p.mu.Unlock()
	})
	return t.Conn.Close()
}

// CloseWrite half-closes the connection when the underlying one supports
// it, as the CONNECT tunnel expects.
func (t *trackedConn) CloseWrite() error {
	if cw, ok := t.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
