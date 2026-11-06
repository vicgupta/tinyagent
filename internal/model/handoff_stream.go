package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// activityFunc is the live tool/command callback installed with WithActivity.
// id is stable for one CLI activity, name is a short label, and chunk is new
// text (not a replacement of earlier chunks).
type activityFunc func(id, name, chunk string)

type activityKey struct{}

// WithActivity returns a context whose handoff providers report the CLI's
// own tool and command activity as it arrives. The agent attaches the
// callback only while streaming; a nil fn leaves ctx unchanged. Chunks are
// raw CLI text: the caller applies secret redaction and the live-preview cap.
func WithActivity(ctx context.Context, fn func(id, name, chunk string)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, activityKey{}, activityFunc(fn))
}

func activityFrom(ctx context.Context) activityFunc {
	fn, _ := ctx.Value(activityKey{}).(activityFunc)
	return fn
}

// textSink is the assistant text already given to OnText.
type textSink struct {
	onText func(string)
	buf    strings.Builder
}

func (s *textSink) write(t string) {
	if s == nil || s.onText == nil || t == "" {
		return
	}
	s.buf.WriteString(t)
	s.onText(t)
}

// finish gives OnText the parsed final answer when the live events did not
// already end with it, so a plain-text fallback is still shown once and a
// streamed answer is not printed again.
func (s *textSink) finish(final string) {
	if s == nil || s.onText == nil || strings.TrimSpace(final) == "" {
		return
	}
	if alreadyStreamed(s.buf.String(), final) {
		return
	}
	s.onText(final)
}

// alreadyStreamed reports whether streamed text already ends with final, so
// the parsed answer is not sent through OnText a second time.
func alreadyStreamed(streamed, final string) bool {
	if streamed == "" || strings.TrimSpace(final) == "" {
		return false
	}
	s := strings.TrimRight(streamed, " \t\r\n")
	f := strings.TrimRight(final, " \t\r\n")
	return s == f || strings.HasSuffix(s, f)
}

// maxCLILine is the largest stdout line a handoff stream will accept, matching
// the buffered parsers' scanner limit.
const maxCLILine = 64 * 1024 * 1024

// lineBuf is cmd.Stdout. Write is called by the command's stdout copier as
// bytes arrive, and Wait does not return until that copier finishes, so the
// line callback runs before Query returns and there is no StdoutPipe to close
// out from under the reader.
type lineBuf struct {
	full    bytes.Buffer
	pending []byte
	onLine  func(string)
	err     error
}

func (w *lineBuf) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.full.Write(p)
	w.pending = append(w.pending, p...)
	if len(w.pending) > maxCLILine {
		w.err = fmt.Errorf("stdout line exceeds %d bytes", maxCLILine)
		return 0, w.err
	}
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := string(w.pending[:i])
		w.pending = append([]byte(nil), w.pending[i+1:]...)
		if w.onLine != nil && line != "" {
			w.onLine(line)
		}
	}
	return len(p), nil
}

// runCLIStream is runCLIEnv that calls onLine for each stdout line as the
// child writes it. The returned buffers are still the full stdout and
// stderr, so the existing result parsers stay the source of truth. A
// cancelled ctx kills the process group the same way runCLI does.
//
// A shell buffers its own stdout when the destination is not a terminal;
// tests emit each line from a short-lived child so the line is flushed
// before the script continues.
func runCLIStream(ctx context.Context, provider, path string, args []string, dir, stdin string, env []string, onLine func(string)) (cliResult, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out lineBuf
	out.onLine = onLine
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	setProcessGroup(cmd)
	cmd.WaitDelay = handoffWaitDelay
	waitErr := cmd.Run()
	res := cliResult{stdout: out.full.Bytes(), stderr: errBuf.Bytes()}
	if ctx.Err() != nil {
		return res, fmt.Errorf("%s: stopped: %w", provider, ctx.Err())
	}
	if out.err != nil {
		return res, fmt.Errorf("%s: read %s: %w", provider, path, out.err)
	}
	var ee *exec.ExitError
	switch {
	case errors.As(waitErr, &ee):
		res.exitCode = ee.ExitCode()
	case waitErr != nil:
		return res, fmt.Errorf("%s: run %s: %w", provider, path, waitErr)
	}
	return res, nil
}

// streamFlagRejected reports whether a streaming invocation died because the
// CLI does not accept flag, before any JSON event was parsed. A run that
// produced events is a real failure and is not retried.
func streamFlagRejected(res cliResult, sawEvent bool, flags ...string) bool {
	if sawEvent || res.exitCode == 0 {
		return false
	}
	out := string(res.stderr) + "\n" + string(res.stdout)
	for _, flag := range flags {
		if unrecognizedFlag(out, flag) || invalidFlagValue(out, flag) {
			return true
		}
	}
	return false
}

// invalidFlagValue reports a rejection such as
// "invalid value 'stream-json' for '--output-format'".
func invalidFlagValue(output, flag string) bool {
	o := strings.ToLower(output)
	if !strings.Contains(o, "invalid value") && !strings.Contains(o, "invalid choice") {
		return false
	}
	return strings.Contains(o, "--"+flag) || strings.Contains(o, flag)
}
