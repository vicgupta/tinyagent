package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// SandboxHelperArg is argv[1] of the re-executed tinyagent that applies the
// Linux sandbox and then execs the command. It is not a user-facing flag.
const SandboxHelperArg = "__sandbox_exec"

// Sandbox confines bash and start_process commands (and everything they
// spawn) at the OS level: writes are limited to Writable, and outbound
// network is denied unless Network is set, or limited to AllowedDomains
// through a filtering proxy. Reads stay unrestricted.
type Sandbox struct {
	Backend  string   // e.g. "landlock (ABI 6)" or "seatbelt"
	Writable []string // directories commands may write, including Tmp
	Network  bool
	// AllowedDomains, when set, are the only hosts commands may reach, over
	// HTTP and HTTPS through a proxy tinyagent runs outside the sandbox.
	AllowedDomains []string
	Tmp            string // per-run temporary directory, exported as TMPDIR

	proxy    *sandboxnet.Proxy
	proxyDir string   // private directory holding the proxy's Unix socket (Linux)
	proxyEnv []string // proxy variables added to every command's environment
}

// NewSandbox checks that this host can enforce the policy and creates the
// per-run temp directory. It fails rather than returning a weaker sandbox.
// root and extra must be existing directories. A non-empty allowed list
// (sandbox_allowed_domains entries) starts the filtering proxy; it can't be
// combined with network.
func NewSandbox(root string, extra []string, network bool, allowed []string) (*Sandbox, error) {
	rules, err := sandboxnet.ParseRules(allowed)
	if err != nil {
		return nil, fmt.Errorf("sandbox: sandbox_allowed_domains %v", err)
	}
	proxied := len(rules) > 0
	if network && proxied {
		return nil, errors.New("sandbox: sandbox_allowed_domains can't be combined with sandbox_network")
	}
	backend, err := probeSandbox(network, proxied)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "tinyagent-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("sandbox: creating temp directory: %w", err)
	}
	s := &Sandbox{Backend: backend, Network: network, AllowedDomains: allowed}
	for _, d := range append([]string{root, tmp}, extra...) {
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("sandbox: writable path %s: %w", d, err)
		}
		s.Writable = append(s.Writable, r)
		if d == tmp {
			s.Tmp = r
		}
	}
	if proxied {
		ln, url, err := s.listenProxy()
		if err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("sandbox: starting the network allowlist proxy: %w", err)
		}
		s.proxy = sandboxnet.NewProxy(rules, ln)
		go s.proxy.Serve()
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
			s.proxyEnv = append(s.proxyEnv, k+"="+url, strings.ToLower(k)+"="+url)
		}
		s.proxyEnv = append(s.proxyEnv, "NO_PROXY=", "no_proxy=")
	}
	return s, nil
}

// Cleanup stops the allowlist proxy and removes the per-run temp directory.
func (s *Sandbox) Cleanup() {
	if s == nil {
		return
	}
	if s.proxy != nil {
		s.proxy.Close()
	}
	if s.proxyDir != "" {
		os.RemoveAll(s.proxyDir)
	}
	if s.Tmp != "" {
		os.RemoveAll(s.Tmp)
	}
}

// TakeNetworkDenials returns the requests the allowlist proxy refused since
// the last call.
func (s *Sandbox) TakeNetworkDenials() []sandboxnet.Denial {
	if s == nil || s.proxy == nil {
		return nil
	}
	return s.proxy.TakeDenials()
}

// NetworkMode describes the network policy: denied, allowed, or allowlist
// with its entries.
func (s *Sandbox) NetworkMode() string {
	switch {
	case s.Network:
		return "allowed"
	case len(s.AllowedDomains) == 1:
		return "allowlist (1 domain: " + s.AllowedDomains[0] + ")"
	case len(s.AllowedDomains) > 1:
		return fmt.Sprintf("allowlist (%d domains: %s)", len(s.AllowedDomains), strings.Join(s.AllowedDomains, ", "))
	}
	return "denied"
}

// Describe is the one-line summary printed at startup.
func (s *Sandbox) Describe() string {
	return fmt.Sprintf("sandbox: %s; writable: %s; network: %s", s.Backend, strings.Join(s.Writable, ", "), s.NetworkMode())
}

// bashPath resolves bash with tinyagent's own PATH, so the sandbox wrapper
// finds it even when command_env withholds PATH from the command.
func bashPath() string {
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "bash"
}

var errSandboxUnsupported = errors.New("sandbox: not supported on this operating system (supported: Linux with Landlock, macOS); run without --sandbox")

// devFiles are device files that ordinary tools write to.
var devFiles = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/tty", "/dev/random", "/dev/urandom"}
