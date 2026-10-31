//go:build windows

package model

import "os/exec"

// Windows has no process groups in this sense; the default Cancel kills the
// CLI process only.
func setProcessGroup(cmd *exec.Cmd) {}
