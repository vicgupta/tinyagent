package tools

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
)

// MaxLiveOutputBytes caps each call's live preview, independently of its result.
const MaxLiveOutputBytes = 256 << 10

const liveTruncated = "\n[live output truncated]\n"

type outputKey struct{}
type liveWriterKey struct{}
type outputCallback struct{ emit func(string) }

// WithOutput scopes a live output callback to a tool execution. A nil callback
// explicitly disables streaming, including Executor.Stream. Without WithOutput,
// direct callers may still use Executor.Stream with bash's stream: true.
func WithOutput(ctx context.Context, emit func(string)) context.Context {
	return context.WithValue(ctx, outputKey{}, outputCallback{emit})
}

func outputWriter(ctx context.Context) io.Writer {
	w, _ := ctx.Value(liveWriterKey{}).(*liveOutput)
	if w == nil {
		return nil
	}
	return w
}

func (e *Executor) liveOutput(ctx context.Context, call model.ToolCall) *liveOutput {
	name := call.Function.Name
	if name != "bash" && name != "read_process" && e.commandTool(name) == nil {
		return nil
	}
	var a struct {
		Stream *bool `json:"stream"`
	}
	if name == "bash" {
		if json.Unmarshal([]byte(call.Function.Arguments), &a) != nil || (a.Stream != nil && !*a.Stream) {
			return nil
		}
	}
	var emit func(string)
	if cb, ok := ctx.Value(outputKey{}).(outputCallback); ok {
		emit = cb.emit
	} else if name == "bash" && a.Stream != nil && *a.Stream && e.Stream != nil {
		emit = func(s string) { _, _ = io.WriteString(e.Stream, s) }
	}
	if emit == nil {
		return nil
	}
	return &liveOutput{emit: emit, red: e.Redactor}
}

// liveOutput is private to one RunResult. exec.Cmd serializes writes to the
// shared stdout/stderr writer and waits for them before RunResult flushes it.
// Sink errors never interrupt the command or alter its authoritative result.
// With redaction on, complete lines are held until safe to redact together;
// an oversized unterminated line is discarded rather than leaked or retained.
type liveOutput struct {
	emit    func(string)
	red     *eventlog.Redactor
	pending string
	read    int
	sent    int
	stopped bool
}

// Bearer tokens may span whitespace/newlines. Hold the introducer until the
// token arrives, just as PartialSuffix holds known multiline secret values.
var liveBearerSuffix = regexp.MustCompile(`(?i)\bbearer\s*$`)

func (w *liveOutput) Write(p []byte) (int, error) {
	n := len(p)
	if w.stopped {
		return n, nil
	}
	remaining := MaxLiveOutputBytes - w.read
	take := min(n, remaining)
	w.read += take
	w.pending += string(p[:take])
	end := len(w.pending)
	if w.red != nil {
		end = strings.LastIndexByte(w.pending, '\n') + 1
		if hold := w.red.PartialSuffix(w.pending[:end]); hold > 0 {
			end = strings.LastIndexByte(w.pending[:end-hold], '\n') + 1
		}
		if loc := liveBearerSuffix.FindStringIndex(w.pending[:end]); loc != nil {
			end = strings.LastIndexByte(w.pending[:loc[0]], '\n') + 1
		}
	} else {
		// Do not split a UTF-8 rune between JSON events.
		if end > 0 {
			start := end - 1
			for start > 0 && !utf8.RuneStart(w.pending[start]) {
				start--
			}
			if !utf8.FullRuneInString(w.pending[start:]) {
				end = start
			}
		}
	}
	w.flush(end)
	if n > take {
		w.truncate()
	}
	return n, nil
}

func (w *liveOutput) flush(end int) {
	if end == 0 || w.stopped {
		return
	}
	s := w.pending[:end]
	w.pending = w.pending[end:]
	if w.red != nil {
		s = w.red.Redact(s)
	}
	// Reserve room for the marker, so even truncation cannot exceed the cap.
	remaining := MaxLiveOutputBytes - len(liveTruncated) - w.sent
	if len(s) > remaining {
		s = s[:remaining]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
		if s != "" {
			w.emit(s)
			w.sent += len(s)
		}
		w.truncate()
		return
	}
	w.emit(s)
	w.sent += len(s)
}

func (w *liveOutput) truncate() {
	if !w.stopped {
		w.emit(liveTruncated)
		w.pending = ""
		w.stopped = true
	}
}

func (w *liveOutput) Close() { w.flush(len(w.pending)) }
