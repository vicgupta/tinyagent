//go:build linux

package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The bwrap engine runs each command under Bubblewrap: the whole filesystem
// read-only, the writable paths bound read-write, private /proc, /dev and
// /tmp, new user, PID, IPC and UTS namespaces, and a new network namespace
// unless commands have direct network. Inside it, the Landlock helper (see
// wrap in sandbox_linux.go) applies the same write and network rules again
// before it execs the command, so either layer alone still confines it.

// bwrapLook finds bwrap; bwrapRun runs a probe command. Tests replace them.
var (
	bwrapLook = func() (string, error) { return exec.LookPath("bwrap") }
	bwrapRun  = func(path string, args []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, path, args...).CombinedOutput()
	}
	// landlockProbe is probeSandbox; tests replace it.
	landlockProbe = probeSandbox
)

// bwrapProbeArgs are the namespaces and mounts every command uses, around
// a trivial command.
func bwrapProbeArgs() []string {
	args := append(bwrapBaseArgs(false), "--", "/bin/sh", "-c", ":")
	return args
}

// bwrapBaseArgs are the bwrap options shared by every command; network
// keeps the host network namespace.
func bwrapBaseArgs(network bool) []string {
	args := []string{"--die-with-parent", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts"}
	if !network {
		args = append(args, "--unshare-net")
	}
	return append(args, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc")
}

// findBwrap returns the path of a bwrap that can create the sandbox, or an
// error explaining why not (with the AppArmor fix where that is the cause).
func findBwrap() (string, error) {
	path, err := bwrapLook()
	if err != nil {
		return "", errBwrapMissing
	}
	if out, err := bwrapRun(path, bwrapProbeArgs()); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			err = fmt.Errorf("%v: %s", err, msg)
		}
		return "", bwrapProbeError(err, apparmorUsernsPath)
	}
	return path, nil
}

var errBwrapMissing = errors.New("sandbox: bwrap (Bubblewrap) not found on PATH; install it (e.g. apt install bubblewrap) or use sandbox_backend: landlock")

// sandboxEngine is the backend selectSandbox picked.
type sandboxEngine struct {
	desc     string // e.g. "bwrap + landlock (ABI 6)"
	bwrap    string // bwrap path; empty for Landlock alone
	landlock bool   // run the Landlock helper (inside bwrap when bwrap is set)
	warnings []string
}

// selectSandbox picks the engine for backend (auto, landlock or bwrap).
// auto prefers bwrap with Landlock stacked inside and falls back to
// Landlock alone, warning when bwrap is installed but blocked. Under bwrap
// without Landlock, commands still run confined by bwrap alone when the
// network is fully on or off; the allowlist needs the Landlock helper.
func selectSandbox(backend string, network, proxied bool) (sandboxEngine, error) {
	if !ValidSandboxBackend(backend) {
		return sandboxEngine{}, fmt.Errorf("sandbox: sandbox_backend %q must be auto, landlock or bwrap", backend)
	}
	if backend == "landlock" {
		d, err := landlockProbe(network, proxied)
		return sandboxEngine{desc: d, landlock: true}, err
	}
	path, berr := findBwrap()
	if berr != nil {
		if backend == "bwrap" {
			return sandboxEngine{}, berr
		}
		d, err := landlockProbe(network, proxied)
		if err != nil {
			return sandboxEngine{}, err
		}
		e := sandboxEngine{desc: d, landlock: true}
		if !errors.Is(berr, errBwrapMissing) {
			e.warnings = append(e.warnings, "bwrap unavailable, using Landlock alone: "+strings.TrimPrefix(berr.Error(), "sandbox: "))
		}
		return e, nil
	}
	// bwrap gives the allowlist its network namespace, so Landlock only
	// needs to confine writes and (without direct network) TCP.
	d, lerr := landlockProbe(network, false)
	if lerr == nil {
		return sandboxEngine{desc: "bwrap + " + d, bwrap: path, landlock: true}, nil
	}
	if proxied {
		return sandboxEngine{}, fmt.Errorf("sandbox: sandbox_allowed_domains under bwrap needs the Landlock helper (Linux 6.7+): %w", lerr)
	}
	return sandboxEngine{desc: "bwrap", bwrap: path, warnings: []string{"Landlock unavailable, using bwrap alone: " + strings.TrimPrefix(lerr.Error(), "sandbox: ")}}, nil
}

// bwrapArgs is the bwrap command line for a command: helper is the
// Landlock helper's argv (nil to run argv directly), proxyDir the
// allowlist proxy's socket directory (empty for none).
func (s *Sandbox) bwrapArgs(network bool, proxyDir string, helper, argv []string) []string {
	args := bwrapBaseArgs(network)
	for _, w := range s.Writable {
		args = append(args, "--bind", w, w)
	}
	if proxyDir != "" {
		args = append(args, "--bind", proxyDir, proxyDir)
	}
	if len(helper) > 0 {
		// The helper binary may live under the private /tmp (go test).
		args = append(args, "--ro-bind", helper[0], helper[0])
		argv = append(append([]string(nil), helper...), argv...)
	}
	// bwrap keeps the caller's working directory (cmd.Dir) and environment.
	return append(append(args, "--"), argv...)
}

// wrapBwrap runs argv under bwrap, through the Landlock helper when the
// engine stacks it. Allowlist mode runs the helper's net-proxy forwarder in
// bwrap's network namespace, which has only loopback.
func (s *Sandbox) wrapBwrap(ctx context.Context, argv []string) *exec.Cmd {
	network, proxy, _ := s.netState()
	var helper []string
	proxyDir := ""
	if s.landlock {
		mode := "net-deny"
		switch {
		case network:
			mode = "net-allow"
		case proxy != nil:
			sock := proxy.Addr().String()
			mode = fmt.Sprintf("net-proxy=%d:%s", proxyPort, sock)
			proxyDir = filepath.Dir(sock)
		}
		helper = append([]string{helperPath(), SandboxHelperArg, mode}, s.Writable...)
		helper = append(helper, "--")
	}
	return exec.CommandContext(ctx, s.bwrap, s.bwrapArgs(network, proxyDir, helper, argv)...)
}

// ProbeSandbox reports the engine NewSandboxBackend would pick, without
// creating anything, for --check.
func ProbeSandbox(backend string, network bool, allowed []string) (string, []string, error) {
	e, err := selectSandbox(backend, network, len(allowed) > 0)
	return e.desc, e.warnings, err
}
