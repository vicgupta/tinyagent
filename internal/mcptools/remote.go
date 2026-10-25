package mcptools

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/vicgupta/tinyagent/internal/config"
)

// reservedHeaders are set by tinyagent or the transport and may not be
// configured in headers_env.
var reservedHeaders = map[string]bool{
	"Accept": true, "Content-Type": true, "Mcp-Session-Id": true,
	"Mcp-Protocol-Version": true, "Host": true, "Content-Length": true,
}

// validateRemote checks a url server definition without contacting it.
func validateRemote(def config.MCPServer) error {
	if len(def.Args) > 0 || len(def.Env) > 0 {
		return fmt.Errorf("mcp server %q: args and env apply only to command servers, not url servers", def.Alias)
	}
	u, err := url.Parse(def.URL)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("mcp server %q: url must be an absolute http(s) URL", def.Alias)
	}
	if u.User != nil {
		return fmt.Errorf("mcp server %q: url must not contain credentials (user:pass@); use bearer_token_env or headers_env", def.Alias)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return fmt.Errorf("mcp server %q: url must use https (plain http is allowed only for localhost, 127.0.0.1 or ::1)", def.Alias)
		}
	default:
		return fmt.Errorf("mcp server %q: url must use https (plain http is allowed only for localhost, 127.0.0.1 or ::1)", def.Alias)
	}
	if u.Fragment != "" {
		return fmt.Errorf("mcp server %q: url must not contain a fragment", def.Alias)
	}
	if def.BearerTokenEnv != "" {
		if err := checkEnvName(def.Alias, def.BearerTokenEnv); err != nil {
			return err
		}
	}
	for name, env := range def.HeadersEnv {
		if !validHeaderName(name) {
			return fmt.Errorf("mcp server %q: headers_env has an invalid header name %q", def.Alias, name)
		}
		canon := textproto.CanonicalMIMEHeaderKey(name)
		if reservedHeaders[canon] {
			return fmt.Errorf("mcp server %q: headers_env may not set %s, which tinyagent or the transport controls", def.Alias, canon)
		}
		if canon == "Authorization" && def.BearerTokenEnv != "" {
			return fmt.Errorf("mcp server %q: headers_env sets Authorization while bearer_token_env is also set", def.Alias)
		}
		if err := checkEnvName(def.Alias, env); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for name := range def.HeadersEnv {
		canon := textproto.CanonicalMIMEHeaderKey(name)
		if seen[canon] {
			return fmt.Errorf("mcp server %q: headers_env sets header %s more than once", def.Alias, canon)
		}
		seen[canon] = true
	}
	return nil
}

// checkEnvName requires a credential variable to be set and nonempty. Errors
// name the variable, never its value.
func checkEnvName(alias, name string) error {
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return fmt.Errorf("mcp server %q: invalid credential environment variable name %q", alias, name)
	}
	if os.Getenv(name) == "" {
		return fmt.Errorf("mcp server %q: credential environment variable %s is unset or empty", alias, name)
	}
	return nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		// RFC 9110 token characters.
		if r > 0x7e || r <= 0x20 || strings.ContainsRune("\"(),/:;<=>?@[\\]{}", r) {
			return false
		}
	}
	return true
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// origin returns scheme://host[:port] for u, with no path or query.
func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// credentialEnvNames lists a definition's credential variable names, sorted.
func credentialEnvNames(def config.MCPServer) []string {
	var names []string
	if def.BearerTokenEnv != "" {
		names = append(names, def.BearerTokenEnv)
	}
	for _, env := range def.HeadersEnv {
		names = append(names, env)
	}
	sort.Strings(names)
	return names
}

// CredentialValues returns the current values of every credential variable
// named by url servers in cfg, so logs can redact them.
func CredentialValues(cfg []config.MCPServer) []string {
	var vals []string
	for _, def := range cfg {
		for _, name := range credentialEnvNames(def) {
			if v := os.Getenv(name); v != "" {
				vals = append(vals, v)
			}
		}
	}
	return vals
}

// remoteTransport adds the configured headers to requests for exactly one
// origin, refuses requests for any other origin, and remembers the last HTTP
// status or transport error for startup diagnostics.
type remoteTransport struct {
	base    http.RoundTripper
	origin  string
	headers http.Header

	mu         sync.Mutex
	lastStatus int
	lastType   string
	lastErr    error
	redirect   bool
}

func (t *remoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if origin(req.URL) != t.origin {
		return nil, errors.New("request to a different origin refused")
	}
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header[k] = append([]string(nil), v...)
	}
	resp, err := t.base.RoundTrip(req)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		if req.Context().Err() == nil {
			t.lastErr = err
		}
		return nil, err
	}
	t.lastStatus = resp.StatusCode
	t.lastType = resp.Header.Get("Content-Type")
	return resp, nil
}

// newRemoteClient builds the HTTP client for one validated url server.
func newRemoteClient(def config.MCPServer) (*http.Client, *remoteTransport) {
	u, _ := url.Parse(def.URL)
	h := http.Header{}
	if def.BearerTokenEnv != "" {
		h.Set("Authorization", "Bearer "+os.Getenv(def.BearerTokenEnv))
	}
	for name, env := range def.HeadersEnv {
		h.Set(name, os.Getenv(env))
	}
	rt := &remoteTransport{base: http.DefaultTransport.(*http.Transport).Clone(), origin: origin(u), headers: h}
	client := &http.Client{
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if origin(req.URL) != rt.origin {
				rt.mu.Lock()
				rt.redirect = true
				rt.mu.Unlock()
				return errors.New("redirect to a different origin refused")
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	return client, rt
}

// startupError explains a failed connection or discovery for a url server
// using what the transport observed. Only the URL origin is shown.
func (t *remoteTransport) startupError(def config.MCPServer, stage string, err error) error {
	t.mu.Lock()
	status, ctype, lastErr, redirect := t.lastStatus, t.lastType, t.lastErr, t.redirect
	t.mu.Unlock()
	detail := redact(err.Error(), def.URL, t.origin)
	prefix := fmt.Sprintf("mcp server %q (%s): %s failed", def.Alias, t.origin, stage)
	switch {
	case redirect:
		return fmt.Errorf("%s: the server redirected to a different origin; credentials are never forwarded, so use the final URL directly", prefix)
	case lastErr != nil && isTLSError(lastErr):
		return fmt.Errorf("%s: TLS verification failed: %s", prefix, redact(lastErr.Error(), def.URL, t.origin))
	case lastErr != nil:
		return fmt.Errorf("%s: could not reach the server (connection refused, DNS failure, or timeout): %s", prefix, redact(lastErr.Error(), def.URL, t.origin))
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		hint := "the server requires credentials; set bearer_token_env or headers_env"
		if names := credentialEnvNames(def); len(names) > 0 {
			hint = "check the credential in environment variable " + strings.Join(names, ", ")
		}
		return fmt.Errorf("%s: HTTP %d from the server; %s", prefix, status, hint)
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return fmt.Errorf("%s: HTTP %d from the server; the URL may not be a Streamable HTTP MCP endpoint", prefix, status)
	case status >= 200 && status < 300 && ctype != "" && !strings.HasPrefix(ctype, "application/json") && !strings.HasPrefix(ctype, "text/event-stream"):
		return fmt.Errorf("%s: the server answered with %s, not an MCP response; the URL may not be a Streamable HTTP MCP endpoint", prefix, safeMIME(ctype))
	case status >= 400:
		return fmt.Errorf("%s: HTTP %d from the server: %s", prefix, status, detail)
	}
	return fmt.Errorf("%s: %s", prefix, detail)
}

func isTLSError(err error) bool {
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	var rh tls.RecordHeaderError
	var ae tls.AlertError
	return errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci) || errors.As(err, &rh) || errors.As(err, &ae)
}

// redact replaces the full configured URL with its origin so paths and
// query strings (which may carry tokens) are not echoed.
func redact(s, full, origin string) string {
	if full != "" && full != origin {
		s = strings.ReplaceAll(s, full, origin)
	}
	return s
}
