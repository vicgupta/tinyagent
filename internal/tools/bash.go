package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Bash runs command in a fresh `bash -c` process in root. On timeout the
// whole process group is killed so background children do not linger.
func Bash(ctx context.Context, root, command string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = root
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("command timed out after %s and was killed\n<output>\n%s</output>", timeout, buf.String()), nil
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		return "", err
	}
	return fmt.Sprintf("<returncode>%d</returncode>\n<output>\n%s</output>", code, buf.String()), nil
}
