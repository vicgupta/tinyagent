package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lsp"
	"github.com/vicgupta/tinyagent/internal/lsp/lsptest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func lspCall(id, op, path string) model.ToolCall {
	b, _ := json.Marshal(map[string]any{"operation": op, "path": path, "line": 1, "column": 1})
	return call(id, "lsp", string(b))
}

func withLSP(t *testing.T, a *Agent, servers ...config.LSPServer) *lsp.Manager {
	t.Helper()
	m := lsp.New(lsp.Options{Root: a.Tools.Root, Servers: servers})
	t.Cleanup(m.Close)
	a.Tools.LSP = &tools.LSP{Manager: m, AfterEdit: true, Wait: 3 * time.Second, Max: 20}
	return m
}

type lspEvents struct {
	kinds  []string
	server []map[string]any
	warn   []string
}

func (e *lspEvents) hook(kind string, fields map[string]any) {
	e.kinds = append(e.kinds, kind)
	switch kind {
	case "lsp.server":
		e.server = append(e.server, fields)
	case "warning":
		e.warn = append(e.warn, fields["message"].(string))
	}
}

func (e *lspEvents) states() []string {
	var out []string
	for _, f := range e.server {
		out = append(out, f["name"].(string)+" "+f["state"].(string))
	}
	return out
}

func TestLSPOffKeepsSignature(t *testing.T) {
	for _, ro := range []bool{false, true} {
		if LSPToolSignature(ro, false, false, false, false, false, false) != toolSignature(ro, false) {
			t.Errorf("signature changed with lsp off (read-only %v)", ro)
		}
		if s := LSPToolSignature(ro, false, false, false, false, false, true); !strings.HasPrefix(s, "lsp-v1:") {
			t.Errorf("lsp signature %s", s)
		}
	}
}

func TestLSPOffByDefault(t *testing.T) { // AC1
	m := &specModel{scripted: scripted{replies: []model.Message{
		assistantCalls(lspCall("1", "diagnostics", "a.go")),
		{Role: "assistant", Content: "done"},
	}}}
	a := newAgent(t, m)
	tr := a.Run(context.Background(), "x")
	if slices.Contains(toolNames(m.specs[0]), "lsp") {
		t.Errorf("lsp advertised by default")
	}
	if res := toolResults(tr.Messages); len(res) != 1 || !strings.HasPrefix(res[0], "format error: unknown tool") {
		t.Errorf("results = %q", res)
	}
	b, _ := json.Marshal(tr)
	if strings.Contains(string(b), `"lsp_`) || tr.ToolSignature != ToolSignature() {
		t.Errorf("trajectory has lsp fields: %s", b)
	}
}

func TestLSPRunEventsAndResume(t *testing.T) { // AC3, AC10
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "write_file", `{"path":"a.go","content":"package a\nERR: undefined: x\n"}`)),
		assistantCalls(lspCall("2", "diagnostics", "a.go")),
		submit,
	}}
	a := newAgent(t, m)
	withLSP(t, a, lsptest.Server(t, "go", []string{".go"}))
	var errBuf bytes.Buffer
	a.Err, a.Verbose = &errBuf, true
	var ev lspEvents
	var results []string
	a.OnEvent = func(kind string, fields map[string]any) {
		ev.hook(kind, fields)
		if kind == "tool.result" {
			results = append(results, fields["output"].(string))
		}
	}
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted || !tr.LSPEnabled || !strings.HasPrefix(tr.ToolSignature, "lsp-v1:") || !slices.Equal(tr.LSPServers, []string{"go"}) {
		t.Fatalf("status %s, enabled %v, signature %s, servers %v", tr.ExitStatus, tr.LSPEnabled, tr.ToolSignature, tr.LSPServers)
	}
	if !slices.Contains(tr.Tools, "lsp") {
		t.Errorf("tools %v lack lsp", tr.Tools)
	}
	res := toolResults(tr.Messages)
	want := "ok\n[lsp go: 1 error in a.go]\na.go:2:1: undefined: x"
	if len(res) != 2 || res[0] != want || res[1] != "[lsp go: 1 error in a.go]\na.go:2:1: undefined: x" {
		t.Fatalf("results = %q", res)
	}
	if len(results) == 0 || results[0] != want {
		t.Errorf("tool.result output = %q", results)
	}
	if got := ev.states(); !slices.Equal(got, []string{"go started", "go stopped"}) {
		t.Errorf("lsp.server events = %v", got)
	}
	if len(ev.warn) != 0 {
		t.Errorf("warnings = %v", ev.warn)
	}
	if !strings.Contains(errBuf.String(), "lsp: go started") || !strings.Contains(errBuf.String(), "lsp: go stopped") {
		t.Errorf("verbose output = %q", errBuf.String())
	}
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
		t.Errorf("ValidateResume: %v", err)
	}

	// Same setting: the resume continues with a fresh manager.
	withLSP(t, a, lsptest.Server(t, "go", []string{".go"}))
	a.Resume, a.Model, a.FollowUp = tr, &scripted{replies: []model.Message{submit}}, "more"
	r := a.Run(context.Background(), "more")
	if r.ExitStatus != StatusSubmitted {
		t.Fatalf("resume: %s %q", r.ExitStatus, r.Submission)
	}

	a.Tools.LSP = nil
	a.Resume, a.Model = r, &scripted{replies: []model.Message{submit}}
	if r := a.Run(context.Background(), "more"); r.ExitStatus != StatusError || !strings.Contains(r.Submission, "lsp setting differs") {
		t.Errorf("mismatched resume: %s %q", r.ExitStatus, r.Submission)
	}
	tampered := *tr
	tampered.LSPEnabled = false
	if err := ValidateResume(&tampered, a.Config, a.Tools.Root); err == nil {
		t.Error("ValidateResume accepted a changed lsp setting")
	}
	v1 := trajectory.Trajectory{SchemaVersion: 1, LSPEnabled: true}
	if err := v1.Validate(); err == nil || !strings.Contains(err.Error(), "version 2 fields") {
		t.Errorf("schema 1 with lsp: %v", err)
	}
}

func TestLSPMissingBinary(t *testing.T) { // AC11
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "write_file", `{"path":"a.go","content":"package a\n"}`)),
		assistantCalls(call("2", "write_file", `{"path":"b.go","content":"package a\n"}`)),
		assistantCalls(lspCall("3", "diagnostics", "a.go")),
		submit,
	}}
	a := newAgent(t, m)
	withLSP(t, a, config.LSPServer{Name: "go", Command: filepath.Join(a.Tools.Root, "no-such-lsp"), Extensions: []string{".go"}})
	var errBuf bytes.Buffer
	a.Err = &errBuf
	var ev lspEvents
	a.OnEvent = ev.hook
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s %q", tr.ExitStatus, tr.Submission)
	}
	res := toolResults(tr.Messages)
	if len(res) != 3 || res[0] != "ok\n[lsp go: server unavailable; no diagnostics for a.go]" || !strings.HasPrefix(res[2], "error: lsp server go is unavailable") {
		t.Fatalf("results = %q", res)
	}
	if got := ev.states(); !slices.Equal(got, []string{"go unavailable"}) {
		t.Errorf("lsp.server events = %v", got)
	}
	if len(ev.warn) != 1 || strings.Count(errBuf.String(), "warning: lsp server go is unavailable") != 1 {
		t.Errorf("warnings = %v, stderr %q", ev.warn, errBuf.String())
	}
}

func TestLSPCrashRestartsOnce(t *testing.T) { // AC12
	m := &scripted{replies: []model.Message{
		assistantCalls(lspCall("1", "definition", "a.go")),
		assistantCalls(lspCall("2", "definition", "a.go")),
		assistantCalls(lspCall("3", "definition", "a.go")),
		assistantCalls(lspCall("4", "definition", "a.go")),
		submit,
	}}
	a := newAgent(t, m)
	if err := os.WriteFile(filepath.Join(a.Tools.Root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withLSP(t, a, lsptest.Server(t, "go", []string{".go"}, "crash"))
	var ev lspEvents
	a.OnEvent = ev.hook
	start := time.Now()
	tr := a.Run(context.Background(), "task")
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("run took %s", d)
	}
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s %q", tr.ExitStatus, tr.Submission)
	}
	res := toolResults(tr.Messages)
	if len(res) != 4 || !strings.HasPrefix(res[len(res)-1], "error: lsp server go is unavailable") {
		t.Fatalf("results = %q", res)
	}
	starts := 0
	for _, s := range ev.states() {
		if s == "go started" {
			starts++
		}
	}
	if got := ev.states(); starts != 2 || got[len(got)-1] != "go unavailable" {
		t.Errorf("lsp.server events = %v", got)
	}
}

func TestLSPReadOnlyAndNotForSubagents(t *testing.T) { // AC9
	e := &tools.Executor{Root: t.TempDir(), ReadOnly: true, LSP: &tools.LSP{Manager: lsp.New(lsp.Options{})}}
	if !slices.Contains(toolNames(e.Catalog()), "lsp") {
		t.Errorf("read-only catalog %v lacks lsp", toolNames(e.Catalog()))
	}
	a, pm, cm := subagentAgent(t, []model.Message{
		assistantCalls(delegateCall("1", "look around")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, []model.Message{{Role: "assistant", Content: "found it" + submitMarker}})
	withLSP(t, a, lsptest.Server(t, "go", []string{".go"}))
	a.Run(context.Background(), "x")
	if !slices.Contains(toolNames(pm.specs[0]), "lsp") {
		t.Errorf("parent tools %v lack lsp", toolNames(pm.specs[0]))
	}
	if len(cm.specs) == 0 {
		t.Fatal("child never ran")
	}
	if slices.Contains(toolNames(cm.specs[0]), "lsp") {
		t.Errorf("child offered lsp: %v", toolNames(cm.specs[0]))
	}
}
