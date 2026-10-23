//go:build windows

package tools

import "os/exec"

// Windows has no process groups in this sense; the default Cancel kills bash only.
func setProcessGroup(cmd *exec.Cmd) {}
