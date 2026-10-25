//go:build !linux && !darwin

package tools

import (
	"context"
	"net"
	"os/exec"
)

func probeSandbox(network, proxied bool) (string, error) { return "", errSandboxUnsupported }

func (s *Sandbox) listenProxy() (net.Listener, string, error) { return nil, "", errSandboxUnsupported }

func (s *Sandbox) wrap(ctx context.Context, command string) *exec.Cmd {
	panic("sandbox: unsupported platform")
}

// RunSandboxHelper is only used on Linux.
func RunSandboxHelper(args []string) int { return 2 }
