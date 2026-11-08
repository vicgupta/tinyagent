package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

type toolStdinGate struct {
	tools.Local
	in io.Reader
}

func (e toolStdinGate) Command(ctx context.Context, root, command string) *exec.Cmd {
	cmd := e.Local.Command(ctx, root, command)
	cmd.Stdin = e.in
	return cmd
}

func TestJSONToolOutputWhileRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	in, release := io.Pipe()
	defer in.Close()
	defer release.Close()
	a := newTestAgent(t, &replies{
		{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "build", Type: "function", Function: model.FunctionCall{Name: "bash", Arguments: `{"command":"printf 'building\\n'; read -r line; printf 'done\\n'"}`}}}},
		{Role: "assistant", Content: "done\n" + agent.SubmitMarker},
	})
	a.Streamed = true
	a.Tools.Env = toolStdinGate{in: in}
	out := &toolSignalWriter{signalWriter: signalWriter{seen: make(chan struct{})}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := startEvents(out, true, a, false, cancel, nil, nil)
	done := make(chan *trajectory.Trajectory, 1)
	go func() { done <- a.Run(ctx, "build") }()
	select {
	case <-out.seen:
	case <-ctx.Done():
		t.Fatal("tool.output did not arrive while tool was blocked")
	}
	select {
	case <-done:
		t.Fatal("tool completed before release")
	default:
	}
	io.WriteString(release, "go\n")
	release.Close()
	tr := <-done
	if tr.ExitStatus != agent.StatusSubmitted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	if err := events.finish(tr, time.Second); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range otelEvents(t, out.buf.String()) {
		kind := ev["type"].(string)
		if strings.HasPrefix(kind, "tool.") {
			kinds = append(kinds, kind)
			if ev["id"] != "build" || ev["name"] != "bash" {
				t.Fatalf("wrong call attribution: %v", ev)
			}
		}
	}
	if strings.Join(kinds, " ") != "tool.call tool.output tool.output tool.result" {
		t.Fatalf("tool events = %v", kinds)
	}
}

// signalWriter's locking also protects the embedded buffer while JSONL writes
// come from the command's output copier.
type toolSignalWriter struct{ signalWriter }

func (w *toolSignalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if strings.Contains(string(p), `"type":"tool.output"`) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

func TestCLIToolOutputLiveOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	for _, mode := range []string{"default", "true", "false", "no-stream", "command", "command-no-stream"} {
		t.Run(mode, func(t *testing.T) {
			clearOTelEnv(t)
			r := newOTLPReceiver(t)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", r.url)
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
			root := acpWorkspace(t)
			cfg := writeConfig(t, "model: openai/gpt-4o\n")
			args := `{"command":"printf 'live-sentinel\\n'"}`
			name := "bash"
			if mode == "true" || mode == "no-stream" {
				args = `{"command":"printf 'live-sentinel\\n'","stream":true}`
			} else if mode == "false" {
				args = `{"command":"printf 'live-sentinel\\n'","stream":false}`
			} else if strings.HasPrefix(mode, "command") {
				cfg = runTestsConfig(t)
				name, args = "run_tests", `{"packages":["./live-sentinel"]}`
			}
			newACPProvider(t, acpReply{tool: name, args: args})
			dbPath := filepath.Join(t.TempDir(), "log.db")
			trajPath := filepath.Join(t.TempDir(), "run.json")
			flags := []string{"-t", "build", "--cwd", root, "-c", cfg, "--json", "--log-db=" + dbPath, "--otel", "-o", trajPath}
			if strings.HasSuffix(mode, "no-stream") {
				flags = append(flags, "--no-stream")
			}
			code, stdout, stderr := runCLI(t, flags...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			var live string
			for _, ev := range otelEvents(t, stdout) {
				if ev["type"] == "tool.output" {
					if ev["id"] != "call_1" || ev["name"] != name {
						t.Fatalf("attribution: %v", ev)
					}
					live += ev["output"].(string)
				}
			}
			wantLive := mode != "false" && !strings.HasSuffix(mode, "no-stream")
			if strings.Contains(live, "live-sentinel") != wantLive {
				t.Fatalf("live output = %q wantLive=%v", live, wantLive)
			}
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var chunks, results int
			if err := db.QueryRow(`SELECT count(*) FROM events WHERE type='tool.output'`).Scan(&chunks); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM events WHERE type='tool.result'`).Scan(&results); err != nil {
				t.Fatal(err)
			}
			if chunks != 0 || results != 1 {
				t.Fatalf("db chunks=%d results=%d", chunks, results)
			}
			tr, err := trajectory.Load(trajPath)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(tr)
			if strings.Contains(string(b), `"tool.output"`) || !strings.Contains(string(b), "live-sentinel") {
				t.Fatalf("trajectory missing final result or persisted chunks: %s", b)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			for _, body := range r.bodies {
				if strings.Contains(string(body), "tool.output") {
					t.Fatal("tool.output persisted to OTLP")
				}
			}
		})
	}
}

func TestOutputSchemaWithLiveBashKeepsStdoutClean(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit", true: "explicit"}[explicit], func(t *testing.T) {
			dir, schema := schemaSetup(t, cliSchema)
			args := map[string]any{"command": "printf 'live-progress\\n'", "stream": explicit}
			if !explicit {
				delete(args, "stream")
			}
			b, _ := json.Marshal(args)
			newACPProvider(t, acpReply{tool: "bash", args: string(b)}, acpReply{tool: "submit_result", args: `{"verdict":"pass"}`})
			code, stdout, stderr := runCLI(t, "-t", "x", "--cwd", dir, "-c", filepath.Join(dir, "none.yaml"), "--output-schema", schema, "-v")
			if code != 0 || stdout != "{\"verdict\":\"pass\"}\n" || !strings.Contains(stderr, "live-progress\n") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestHumanToolProgressRouting(t *testing.T) {
	for _, mode := range []string{"short", "verbose", "explicit", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			a := &agent.Agent{}
			var explicit, progress strings.Builder
			o := options{verbose: mode == "verbose", noStream: mode == "disabled"}
			streamToolProgress(a, o, &explicit, &progress)
			if mode == "disabled" {
				if a.OnEvent != nil {
					t.Fatal("no-stream installed a progress sink")
				}
				return
			}
			a.OnEvent("tool.call", map[string]any{"arguments": map[string]any{"stream": mode == "explicit"}})
			a.OnEvent("tool.output", map[string]any{"output": "chunk"})
			a.OnEvent("tool.result", nil)
			if (explicit.String() == "chunk") != (mode == "explicit") || (progress.String() == "chunk") != (mode == "verbose") {
				t.Fatalf("explicit=%q stderr=%q", explicit.String(), progress.String())
			}
		})
	}
}

func TestHumanLongToolProgress(t *testing.T) {
	var out strings.Builder
	p := &toolProgress{progress: &out}
	p.start(false)
	p.chunk("early\n")
	if out.Len() != 0 {
		t.Fatal("short call not quiet")
	}
	p.show() // the timer's callback, without a wall-clock sleep
	p.chunk("later\n")
	p.finish()
	p.show() // a timer racing with finish must not resurrect the preview
	if out.String() != "early\nlater\n" {
		t.Fatalf("long progress = %q", out.String())
	}
}

func TestChatToolOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	for _, mode := range []string{"default", "false", "no-stream"} {
		t.Run(mode, func(t *testing.T) {
			args := `{"command":"printf 'chat-live-sentinel\\n'"}`
			var flags []string
			if mode == "false" {
				args = `{"command":"printf 'chat-live-sentinel\\n'","stream":false}`
			} else if mode == "no-stream" {
				flags = []string{"--no-stream"}
			}
			newACPProvider(t, acpReply{tool: "bash", args: args})
			r := runChatTest(t, acpWorkspace(t), strings.NewReader("build\n/exit\n"), nil, flags...)
			if r.code != 0 || strings.Contains(r.out, "chat-live-sentinel\n") || strings.Contains(r.stderr, "chat-live-sentinel\n") != (mode == "default") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", r.code, r.out, r.stderr)
			}
		})
	}
}

func TestWebToolOutputWhileRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash")
	}
	newACPProvider(t, acpReply{tool: "bash", args: `{"command":"printf 'first secret-sentinel\\n'; read -r line; printf 'done\\n'"}`})
	in, release := io.Pipe()
	defer in.Close()
	defer release.Close()
	c := startWeb(t, acpWorkspace(t), "-v")
	c.w.s.env = toolStdinGate{in: in}
	c.w.s.redactor = eventlog.NewValueRedactor([]string{"secret-sentinel"}, false)
	ev := c.events()
	c.post("/api/message", map[string]any{"text": "build"})
	es := until(t, ev, "tool_output")
	outputs := kinds(es, "tool_output")
	if len(outputs) != 1 || outputs[0]["id"] != "call_1" || outputs[0]["name"] != "bash" || outputs[0]["output"] != "first [REDACTED]\n" {
		t.Fatalf("live output = %v", outputs)
	}
	if len(kinds(es, "tool_result")) != 0 || c.state()["running"] != true {
		t.Fatal("tool output was delayed until command completion")
	}
	io.WriteString(release, "go\n")
	release.Close()
	es = until(t, ev, "turn_end")
	if len(kinds(es, "tool_result")) != 1 {
		t.Fatalf("missing final result: %v", es)
	}
}
