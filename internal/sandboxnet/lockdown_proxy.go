package sandboxnet

import "net"

// NewLockdownProxy returns a proxy whose only allowlist is rules, the
// untrusted_content.lockdown.allowed_domains entries (issue #171). The
// lockdown starts it for a sandbox_network sandbox, whose commands lose
// their direct network at taint. It counts as narrowed from the start, and
// a refused host names untrusted_content.lockdown.allowed_domains rather
// than sandbox_allowed_domains.
func NewLockdownProxy(rules []Rule, ln net.Listener) *Proxy {
	p := NewProxy(rules, ln)
	p.notAllowed = ReasonLockdown
	p.narrow.on = true
	p.narrow.rules = append([]Rule(nil), rules...)
	return p
}

// reasonNotAllowed is the denial reason for a host outside the proxy's
// rules.
func (p *Proxy) reasonNotAllowed() string {
	if p.notAllowed != "" {
		return p.notAllowed
	}
	return ReasonNotAllowed
}
