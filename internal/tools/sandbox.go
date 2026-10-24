package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SandboxHelperArg is argv[1] of the re-executed tinyagent that applies the
// Linux sandbox and then execs the command. It is not a user-facing flag.
const SandboxHelperArg = "__sandbox_exec"

// Sandbox confines bash and start_process commands (and everything they
// spawn) at the OS level: writes are limited to Writable, and outbound
// network is denied unless Network is set. Reads stay unrestricted.
type Sandbox struct {
	Backend  string   // e.g. "landlock (ABI 6)" or "seatbelt"
	Writable []string // directories commands may write, including Tmp
	Network  bool
	Tmp      string // per-run temporary directory, exported as TMPDIR
}

// NewSandbox checks that this host can enforce the policy and creates the
// per-run temp directory. It fails rather than returning a weaker sandbox.
// root and extra must be existing directories.
func NewSandbox(root string, extra []string, network bool) (*Sandbox, error) {
	backend, err := probeSandbox(network)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "tinyagent-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("sandbox: creating temp directory: %w", err)
	}
	s := &Sandbox{Backend: backend, Network: network}
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
	return s, nil
}

// Cleanup removes the per-run temp directory.
func (s *Sandbox) Cleanup() {
	if s != nil && s.Tmp != "" {
		os.RemoveAll(s.Tmp)
	}
}

// Describe is the one-line summary printed at startup.
func (s *Sandbox) Describe() string {
	net := "denied"
	if s.Network {
		net = "allowed"
	}
	return fmt.Sprintf("sandbox: %s; writable: %s; network: %s", s.Backend, strings.Join(s.Writable, ", "), net)
}

// shellCommand returns `bash -c command`, run under sb if it is non-nil.
func shellCommand(ctx context.Context, sb *Sandbox, command string) *exec.Cmd {
	if sb == nil {
		return exec.CommandContext(ctx, "bash", "-c", command)
	}
	cmd := sb.wrap(ctx, command)
	cmd.Env = append(os.Environ(), "TMPDIR="+sb.Tmp)
	return cmd
}

var errSandboxUnsupported = errors.New("sandbox: not supported on this operating system (supported: Linux with Landlock, macOS); run without --sandbox")

// devFiles are device files that ordinary tools write to.
var devFiles = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/tty", "/dev/random", "/dev/urandom"}
