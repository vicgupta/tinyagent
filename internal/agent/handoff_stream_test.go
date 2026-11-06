//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// TestHandoffToolPreviewRedactsAndCaps runs a fake claude that emits a tool
// result containing a secret and more than 256 KiB. The live tool.output
// events must be redacted and capped, and the first chunk must arrive while
// the CLI is still blocked.
func TestHandoffToolPreviewRedactsAndCaps(t *testing.T) {
	const secret = "supersecretvalue"
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "gate"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "t1",
			"content": secret + "\n" + strings.Repeat("y", 300<<10),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n" +
		"d=$(dirname \"$0\")\n" +
		"cat >\"$d/stdin\"\n" +
		"sh -c 'cat' <<'END'\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo hi"}}]}}` + "\n" +
		"END\n" +
		"read _ < \"$d/gate\" || exit 0\n" +
		"sh -c 'cat' <<'END'\n" + string(payload) + "\nEND\n" +
		"sh -c 'cat' <<'END'\n" +
		`{"type":"result","subtype":"success","result":"done","session_id":"s-9","usage":{"input_tokens":4,"output_tokens":3}}` + "\n" +
		"END\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	model.SetCLIPaths(path, "")
	t.Cleanup(func() { model.SetCLIPaths("", "") })

	var streamed strings.Builder
	m, err := model.NewWith("claude-code/default", func(s string) { streamed.WriteString(s) }, model.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := newAgent(t, m)
	a.Streamed = true
	a.Tools.Redactor = eventlog.NewValueRedactor([]string{secret}, false)
	first := make(chan string, 1)
	var chunks []string
	a.OnEvent = func(kind string, fields map[string]any) {
		if kind != "tool.output" {
			return
		}
		chunk, _ := fields["output"].(string)
		chunks = append(chunks, chunk)
		select {
		case first <- chunk:
		default:
		}
	}
	done := make(chan *struct {
		status, submission string
		err                error
	}, 1)
	go func() {
		tr := a.Run(context.Background(), "fix it")
		done <- &struct {
			status, submission string
			err                error
		}{tr.ExitStatus, tr.Submission, nil}
	}()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("no tool.output while the CLI was blocked")
	}
	select {
	case <-done:
		t.Fatal("run finished before the gate opened")
	default:
	}
	f, err := os.OpenFile(filepath.Join(dir, "gate"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n")
	f.Close()
	var result struct {
		status, submission string
		err                error
	}
	select {
	case r := <-done:
		result = *r
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish")
	}
	if result.status != StatusCompleted || result.submission != "done" {
		t.Fatalf("status %s submission %q", result.status, result.submission)
	}
	if streamed.String() != "done" {
		t.Fatalf("assistant text = %q", streamed.String())
	}
	joined := strings.Join(chunks, "")
	if strings.Contains(joined, secret) || !strings.Contains(joined, eventlog.Redacted) {
		t.Fatalf("preview leaked or missed the secret: %q", joined[:min(200, len(joined))])
	}
	if !strings.Contains(joined, "\n[live output truncated]\n") {
		t.Fatal("preview was not capped")
	}
	if len(joined) > tools.MaxLiveOutputBytes {
		t.Fatalf("preview %d bytes exceeds %d", len(joined), tools.MaxLiveOutputBytes)
	}
}
