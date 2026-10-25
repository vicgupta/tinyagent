// Package sandboxnet implements the sandbox network allowlist: entry parsing
// and matching, and the filtering HTTP proxy that sandboxed commands use.
package sandboxnet

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Rule is one parsed sandbox_allowed_domains entry.
type Rule struct {
	Host     string     // lowercase hostname, or the suffix after "*." when Wildcard
	Wildcard bool       // matches subdomains of Host only
	Addr     netip.Addr // set for IP literals
	Port     int        // 0 means 80 and 443
}

// ParseRule parses an entry: a hostname, "*.example.com" (subdomains only)
// or an IP literal, with an optional ":port" (IPv6 as "[::1]:port").
func ParseRule(entry string) (Rule, error) {
	s := strings.ToLower(strings.TrimSpace(entry))
	if s == "" {
		return Rule{}, fmt.Errorf("empty entry")
	}
	if s == "*" {
		return Rule{}, fmt.Errorf("the bare wildcard \"*\" would allow every host; use --sandbox-network instead")
	}
	host, port := s, ""
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return Rule{}, fmt.Errorf("missing ]")
		}
		host, port = s[1:end], s[end+1:]
		if port != "" {
			if !strings.HasPrefix(port, ":") {
				return Rule{}, fmt.Errorf("unexpected %q after ]", port)
			}
			port = port[1:]
		}
	case strings.Count(s, ":") == 1:
		host, port, _ = strings.Cut(s, ":")
	}
	var r Rule
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return Rule{}, fmt.Errorf("invalid port %q", port)
		}
		r.Port = p
	}
	if a, err := netip.ParseAddr(host); err == nil {
		r.Addr = a.Unmap()
		return r, nil
	}
	host = strings.TrimSuffix(host, ".")
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		r.Wildcard, host = true, rest
	}
	if !validHostname(host) {
		return Rule{}, fmt.Errorf("not a hostname, *.domain or IP address")
	}
	r.Host = host
	return r, nil
}

// ParseRules parses entries, naming the first invalid one.
func ParseRules(entries []string) ([]Rule, error) {
	rules := make([]Rule, 0, len(entries))
	for _, e := range entries {
		r, err := ParseRule(e)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %v", e, err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

func (r Rule) portOK(port int) bool {
	if r.Port == 0 {
		return port == 80 || port == 443
	}
	return port == r.Port
}

// Allows reports whether the rule matches host (a hostname or IP literal,
// already normalized by normalizeHost) and port.
func (r Rule) Allows(host string, port int) bool {
	if !r.portOK(port) {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return r.Addr.IsValid() && r.Addr == a.Unmap()
	}
	if r.Wildcard {
		return strings.HasSuffix(host, "."+r.Host)
	}
	return !r.Addr.IsValid() && host == r.Host
}

func normalizeHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

// nonPublic are the ranges refused after resolution unless listed literally:
// loopback, private, link-local (cloud metadata), shared and "this" network.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
}

func isNonPublic(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IsNonPublic reports whether a is a loopback, private, link-local (cloud
// metadata), shared, multicast or unspecified address: one that is refused
// after resolution unless listed literally. The web_fetch tool shares it.
func IsNonPublic(a netip.Addr) bool { return isNonPublic(a) }

// NormalizeHost lowercases h and strips a trailing dot and IPv6 brackets,
// the form Rule.Allows expects.
func NormalizeHost(h string) string { return normalizeHost(h) }
