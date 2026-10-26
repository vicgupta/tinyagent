package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Bash runs command in a fresh `bash -c` process in root. On timeout the
// whole process group is killed so background children do not linger.
func Bash(ctx context.Context, root, command string, timeout time.Duration) (string, error) {
	return BashStream(ctx, root, command, timeout, nil)
}

// BashStream is Bash that also copies output to live, if non-nil, as it is
// produced. The returned result is the same as Bash's.
func BashStream(ctx context.Context, root, command string, timeout time.Duration, live io.Writer) (string, error) {
	return bashStream(ctx, nil, root, command, timeout, live)
}

// bashStream is BashStream run in env (nil means Local{}).
func bashStream(ctx context.Context, env Environment, root, command string, timeout time.Duration, live io.Writer) (string, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return runStream(ctx, timeout, live, func(ctx context.Context) *exec.Cmd {
		return environment(env).Command(ctx, root, command)
	})
}

// runStream runs the command newCmd returns for a context that times out
// after timeout, and formats the result as bash does.
func runStream(ctx context.Context, timeout time.Duration, live io.Writer, newCmd func(context.Context) *exec.Cmd) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := newCmd(ctx)
	var buf bytes.Buffer
	var w io.Writer = &buf
	if live != nil {
		w = io.MultiWriter(&buf, live)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.WaitDelay = 2 * time.Second

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

// SetProcessGroup makes cancelling cmd kill its whole process group, as for
// bash. On Windows only the process itself is killed.
func SetProcessGroup(cmd *exec.Cmd) { setProcessGroup(cmd) }
