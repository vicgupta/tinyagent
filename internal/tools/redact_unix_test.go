//go:build !windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactBash(t *testing.T) {
	t.Setenv("FOO_TOKEN", testSecret)
	e := &Executor{Root: t.TempDir(), Redactor: redactor()}
	r := e.RunResult(context.Background(), toolCall("1", "bash", map[string]any{"command": `echo "$FOO_TOKEN"; echo "$FOO_TOKEN" >&2`}))
	if strings.Contains(r.Text, testSecret) || strings.Count(r.Text, "[REDACTED]") != 2 || r.Redactions != 2 {
		t.Errorf("bash result = %q (%d redactions)", r.Text, r.Redactions)
	}
}

// TestRedactSplitProcessOutput writes the secret in two chunks read by
// separate read_process calls; no part of it may show.
func TestRedactSplitProcessOutput(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root, Redactor: redactor()}
	defer e.Close()
	head, tail := testSecret[:6], testSecret[6:]
	id, first := startID(t, e, `printf 'start %s' `+head+`; while [ ! -f go ]; do sleep 0.05; done; printf '%s end\n' `+tail+`; sleep 300`)
	waitFor(t, func() bool {
		p, _ := e.lookup(id)
		_, end, _ := p.out.since(0)
		return end >= len("start ")+len(head)
	})
	second := runTool(t, e, "read_process", map[string]any{"id": id})
	if err := os.WriteFile(filepath.Join(root, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	third := runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 5000})
	all := first + second + third
	if strings.Contains(all, head) || strings.Contains(all, tail) {
		t.Errorf("part of the secret shown:\n%s\n%s\n%s", first, second, third)
	}
	if !strings.Contains(third, "[REDACTED] end") {
		t.Errorf("third read = %q", third)
	}
}

func TestRedactProcessTailAtRunEnd(t *testing.T) {
	e := &Executor{Root: t.TempDir(), Redactor: redactor()}
	id, _ := startID(t, e, `sleep 0.2; printf 'a\n%s\nb\n' `+testSecret+`; printf '%s' `+testSecret[:5]+`; sleep 300`)
	waitFor(t, func() bool {
		p, _ := e.lookup(id)
		s, _, _ := p.out.since(0)
		return strings.HasSuffix(s, testSecret[:5])
	})
	summary := e.Close()
	if strings.Contains(summary, testSecret) || !strings.Contains(summary, "[REDACTED]") {
		t.Errorf("summary = %q", summary)
	}
}
