//go:build !linux && !darwin

package tools

import (
	"context"
	"fmt"
	"net"
	"os/exec"
)

func probeSandbox(network, proxied bool) (string, error) { return "", errSandboxUnsupported }

func (s *Sandbox) listenProxy() (net.Listener, string, error) { return nil, "", errSandboxUnsupported }

func (s *Sandbox) wrap(ctx context.Context, argv []string) *exec.Cmd {
	panic("sandbox: unsupported platform")
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
