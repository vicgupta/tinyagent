//go:build windows

package attempts

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
