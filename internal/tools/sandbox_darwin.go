//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const sandboxExec = "/usr/bin/sandbox-exec"

func probeSandbox(network bool) (string, error) {
	if _, err := exec.LookPath(sandboxExec); err != nil {
		return "", fmt.Errorf("sandbox: %s not found; run without --sandbox", sandboxExec)
	}
	return "seatbelt", nil
}

func (s *Sandbox) wrap(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, sandboxExec, "-p", s.profile(), bashPath(), "-c", command)
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
	if !s.Network {
		b.WriteString("(deny network*)\n")
	}
	return b.String()
}

func sbString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// RunSandboxHelper is only used on Linux.
func RunSandboxHelper(args []string) int { return 2 }
