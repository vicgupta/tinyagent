//go:build darwin

package tools

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

const sandboxExec = "/usr/bin/sandbox-exec"

func probeSandbox(network, proxied bool) (string, error) {
	if _, err := exec.LookPath(sandboxExec); err != nil {
		return "", fmt.Errorf("sandbox: %s not found; run without --sandbox", sandboxExec)
	}
	return "seatbelt", nil
}

func (s *Sandbox) wrap(ctx context.Context, argv []string) *exec.Cmd {
	return exec.CommandContext(ctx, sandboxExec, append([]string{"-p", s.profile()}, argv...)...)
}

func (s *Sandbox) listenProxy() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	return ln, "http://" + ln.Addr().String(), nil
}

func (s *Sandbox) profile() string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n")
	for _, w := range s.Writable {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbString(w))
	}
	for _, f := range devFiles {
		fmt.Fprintf(&b, "(allow file-write* (literal %s))\n", sbString(f))
	}
	network, proxy, _ := s.netState()
	if !network {
		b.WriteString("(deny network*)\n")
	}
	if proxy != nil {
		// Seatbelt matches only localhost or * as the remote host.
		fmt.Fprintf(&b, "(allow network-outbound (remote ip \"localhost:%d\"))\n", proxy.Addr().(*net.TCPAddr).Port)
	}
	return b.String()
}

func sbString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// RunSandboxHelper is only used on Linux.
func RunSandboxHelper(args []string) int { return 2 }

// selectSandbox accepts only the auto backend here; landlock and bwrap are
// Linux engines.
func selectSandbox(backend string, network, proxied bool) (sandboxEngine, error) {
	if !ValidSandboxBackend(backend) {
		return sandboxEngine{}, fmt.Errorf("sandbox: sandbox_backend %q must be auto, landlock or bwrap", backend)
	}
	if backend != "" && backend != "auto" {
		return sandboxEngine{}, fmt.Errorf("sandbox: sandbox_backend %s is only available on Linux; use auto", backend)
	}
	d, err := probeSandbox(network, proxied)
	return sandboxEngine{desc: d}, err
}

type sandboxEngine struct {
	desc     string
	bwrap    string
	landlock bool
	warnings []string
}

// ProbeSandbox reports the engine NewSandboxBackend would pick, for --check.
func ProbeSandbox(backend string, network bool, allowed []string) (string, []string, error) {
	e, err := selectSandbox(backend, network, len(allowed) > 0)
	return e.desc, e.warnings, err
}
