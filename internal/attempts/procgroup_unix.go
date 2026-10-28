//go:build !windows

package attempts

import (
	"os/exec"
	"syscall"
)

// setProcessGroup runs cmd in its own process group and kills the whole
// group when its context ends, so a timed-out check leaves no children.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
