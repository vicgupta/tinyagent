package tools

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
)

// stdinGate gives the command a pipe to block on after its first output. No
// wall-clock sleeps: only receiving live output can unblock the command.
type stdinGate struct {
	Local
	in io.Reader
}

func (e stdinGate) Command(ctx context.Context, root, command string) *exec.Cmd {
	cmd := e.Local.Command(ctx, root, command)
	cmd.Stdin = e.in
	return cmd
}
func (e stdinGate) Exec(ctx context.Context, root string, argv []string) *exec.Cmd {
	cmd := e.Local.Exec(ctx, root, argv)
	cmd.Stdin = e.in
	return cmd
}

func TestToolOutputBeforeCommandExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	for _, name := range []string{"bash", "run_build"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			in, release := io.Pipe()
			defer in.Close()
			defer release.Close()
			e := &Executor{Root: t.TempDir(), Env: stdinGate{in: in}}
			command := "printf 'first\\n'; read -r line; printf 'second\\n' >&2"
			args := map[string]any{"command": command}
			if name != "bash" {
				e.CommandTools = []*CommandTool{commandTool(t, e.Root, config.CommandTool{
					Name: name, Description: "build", Argv: []string{"bash", "-c", command},
					Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				})}
				args = map[string]any{}
			}
			chunks := make(chan string, 10)
			ctx = WithOutput(ctx, func(s string) { chunks <- s })
			done := make(chan Result, 1)
			go func() { done <- e.RunResult(ctx, toolCall("live", name, args)) }()
			select {
			case s := <-chunks:
				if s != "first\n" {
					t.Fatalf("first chunk = %q", s)
				}
			case r := <-done:
				t.Fatalf("command exited before release: %+v", r)
			case <-ctx.Done():
				t.Fatal("no live output while command was blocked")
			}
			select {
			case <-done:
				t.Fatal("command was not still running")
			default:
			}
			if _, err := io.WriteString(release, "continue\n"); err != nil {
				t.Fatal(err)
			}
			release.Close() // let exec.Cmd finish its stdin copier
			select {
			case r := <-done:
				if r.IsError || !strings.Contains(r.Text, "first\nsecond\n") {
					t.Fatalf("result = %+v", r)
				}
			case <-ctx.Done():
				t.Fatal("command did not finish")
			}
		})
	}
}

func TestLiveOutputRedactionAcrossWrites(t *testing.T) {
	for _, secret := range []string{"secret-value-12345", "multi\nline-secret-12345"} {
		for split := 1; split < len(secret); split++ {
			var out strings.Builder
			w := &liveOutput{emit: func(s string) { out.WriteString(s) }, red: eventlog.NewValueRedactor([]string{secret}, true)}
			w.Write([]byte("safe\n" + secret[:split]))
			w.Write([]byte(secret[split:] + "\nafter\n"))
			w.Close()
			if out.String() != "safe\n[REDACTED]\nafter\n" {
				t.Fatalf("secret %q split %d: %q", secret, split, out.String())
			}
		}
	}
	for _, secret := range []string{"sk-1234567890123456789012345", "bearer\nTOKENVALUE123456789"} {
		var out strings.Builder
		w := &liveOutput{emit: func(s string) { out.WriteString(s) }, red: eventlog.NewValueRedactor(nil, true)}
		for _, b := range []byte(secret + "\n") {
			w.Write([]byte{b})
		}
		w.Close()
		if out.String() != "[REDACTED]\n" {
			t.Fatalf("pattern %q: %q", secret, out.String())
		}
	}
}

func TestLiveOutputCapAndUTF8(t *testing.T) {
	for _, red := range []*eventlog.Redactor{nil, eventlog.NewValueRedactor([]string{"secret-value"}, true)} {
		for _, line := range []string{strings.Repeat("x", MaxLiveOutputBytes*2), strings.Repeat("é\n", MaxLiveOutputBytes)} {
			var out strings.Builder
			w := &liveOutput{emit: func(s string) { out.WriteString(s) }, red: red}
			for i := 0; i < len(line); i += 257 {
				p := []byte(line[i:min(i+257, len(line))])
				if n, err := w.Write(p); n != len(p) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			w.Close()
			if out.Len() > MaxLiveOutputBytes || strings.Count(out.String(), liveTruncated) != 1 || !utf8.ValidString(out.String()) {
				t.Fatalf("invalid capped stream: len=%d marker=%d valid=%v", out.Len(), strings.Count(out.String(), liveTruncated), utf8.ValidString(out.String()))
			}
		}
	}
	var chunks []string
	w := &liveOutput{emit: func(s string) { chunks = append(chunks, s) }}
	for _, b := range []byte("hello 世界") {
		w.Write([]byte{b})
	}
	w.Close()
	if strings.Join(chunks, "") != "hello 世界" {
		t.Fatalf("UTF-8 stream = %q", chunks)
	}
	for _, s := range chunks {
		if !utf8.ValidString(s) {
			t.Fatalf("invalid UTF-8 chunk %q", s)
		}
	}
}

func TestExecutorStreamOverridesAndFinalResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	for _, mode := range []string{"default", "true", "false", "disabled", "direct"} {
		t.Run(mode, func(t *testing.T) {
			var out, direct strings.Builder
			e := &Executor{Root: t.TempDir(), Stream: &direct, Redactor: eventlog.NewValueRedactor([]string{"secret-value"}, false)}
			ctx := WithOutput(context.Background(), func(s string) { out.WriteString(s) })
			args := map[string]any{"command": "printf 'secret-value\\n'"}
			switch mode {
			case "true", "direct", "disabled":
				args["stream"] = true
			case "false":
				args["stream"] = false
			}
			if mode == "disabled" {
				ctx = WithOutput(ctx, nil)
			} else if mode == "direct" {
				ctx = context.Background()
			}
			r := e.RunResult(ctx, toolCall("live", "bash", args))
			if r.IsError || r.Text != "<returncode>0</returncode>\n<output>\n[REDACTED]\n</output>" || r.Redactions != 1 {
				t.Fatalf("result changed: %+v", r)
			}
			want := "[REDACTED]\n"
			if mode == "false" || mode == "disabled" || mode == "direct" {
				want = ""
			}
			if out.String() != want || (mode != "direct" && direct.Len() != 0) || (mode == "direct" && direct.String() != "[REDACTED]\n") {
				t.Fatalf("callback=%q direct=%q", out.String(), direct.String())
			}
		})
	}
	// A live cap must not affect the final output sent to the model.
	e := &Executor{Root: t.TempDir()}
	call := toolCall("large", "bash", map[string]any{"command": "printf '%0300000d' 0"})
	var live strings.Builder
	r := e.RunResult(WithOutput(context.Background(), func(s string) { live.WriteString(s) }), call)
	plain := e.RunResult(context.Background(), call)
	if r.Text != plain.Text || strings.Contains(r.Text, "live output truncated") || live.Len() > MaxLiveOutputBytes {
		t.Fatal("live preview changed final result")
	}
}

func TestReadProcessWaitLiveOutput(t *testing.T) {
	e := &Executor{}
	p := &process{id: "p1", out: newOutBuffer(1024), done: make(chan struct{})}
	e.procs.procs = map[string]*process{"p1": p}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output := make(chan string, 1)
	release := make(chan struct{})
	done := make(chan Result, 1)
	ctx = WithOutput(ctx, func(s string) { output <- s; <-release })
	go func() {
		done <- e.RunResult(ctx, toolCall("read", "read_process", map[string]any{"id": "p1", "timeout": 5000}))
	}()
	p.out.Write([]byte("waiting output\n"))
	select {
	case s := <-output:
		if s != "waiting output\n" {
			t.Errorf("chunk = %q", s)
		}
	case <-ctx.Done():
		t.Error("no read_process output")
	}
	close(release)
	r := <-done
	if !strings.Contains(r.Text, "waiting output\n") || p.read != len("waiting output\n") {
		b, _ := json.Marshal(r)
		t.Fatalf("result=%s read=%d", b, p.read)
	}
}

func TestToolOutputScopedPerCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	e := &Executor{Root: t.TempDir()}
	type output struct{ want, got string }
	done := make(chan output, 2)
	for _, text := range []string{"one", "two"} {
		go func() {
			var live strings.Builder
			ctx := WithOutput(context.Background(), func(s string) { live.WriteString(s) })
			e.RunResult(ctx, toolCall(text, "bash", map[string]any{"command": "printf " + text}))
			done <- output{text, live.String()}
		}()
	}
	for range 2 {
		r := <-done
		if r.got != r.want {
			t.Fatalf("call %s received %q", r.want, r.got)
		}
	}
}
