//go:build !linux && !darwin

package tools

import (
	"context"
	"os/exec"
)

func probeSandbox(network bool) (string, error) { return "", errSandboxUnsupported }

func (s *Sandbox) wrap(ctx context.Context, command string) *exec.Cmd {
	panic("sandbox: unsupported platform")
}

// RunSandboxHelper is only used on Linux.
func RunSandboxHelper(args []string) int { return 2 }
