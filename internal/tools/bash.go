package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/gitwt"
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
	repo := gitwt.Capture(root)
	out, err := runStream(ctx, timeout, live, func(ctx context.Context) *exec.Cmd {
		return environment(env).Command(ctx, root, command)
	})
	return finishRepoGuard(repo, out, err)
}

// finishRepoGuard undoes a tool command's commits of tinyagent.yaml. A
// failure to start the command is returned unchanged. The check does not
// parse the command: nested shells, command git, absolute paths and
// container exec are covered because it looks at the repository afterward.
func finishRepoGuard(repo *gitwt.RepoState, out string, err error) (string, error) {
	if err != nil {
		return out, err
	}
	note, gerr := repo.Enforce()
	if gerr != nil {
		note = strings.TrimSpace(note + "\ntinyagent: could not undo a commit of tinyagent.yaml: " + gerr.Error())
	}
	if note == "" {
		return out, nil
	}
	out = strings.Replace(out, "<returncode>0</returncode>", "<returncode>1</returncode>", 1)
	if strings.HasSuffix(out, "</output>") {
		out = strings.TrimSuffix(out, "</output>")
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out + note + "\n</output>", nil
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + note + "\n", nil
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
		return fmt.Sprintf("command timed out after %s and was killed; retry with a higher timeout (1-%d seconds) or split the work into smaller commands\n<output>\n%s</output>", timeout, maxBashTimeoutSeconds, buf.String()), nil
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		return "", Actionablef("check that the executable exists on PATH and the command is valid bash", "could not start command: %v", err)
	}
	return fmt.Sprintf("<returncode>%d</returncode>\n<output>\n%s</output>", code, buf.String()), nil
}

// SetProcessGroup makes cancelling cmd kill its whole process group, as for
// bash. On Windows only the process itself is killed.
func SetProcessGroup(cmd *exec.Cmd) { setProcessGroup(cmd) }
