package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// scopedFiles writes files (slash paths relative to root) before the run.
func scopedFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// scopedEvent is one event an agent emitted.
type scopedEvent struct {
	kind   string
	fields map[string]any
}

// scopedRun runs steps (then "done") on an agent with --scoped-agents-md
// on and files written first. cfg adjusts the agent before the run.
func scopedRun(t *testing.T, files map[string]string, cfg func(*Agent), steps ...model.Message) (*trajectory.Trajectory, *Agent, []scopedEvent, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	m := &scripted{replies: append(steps, model.Message{Role: "assistant", Content: "done"})}
	a := newAgent(t, m)
	a.ScopedAgentsMD = true
	scopedFiles(t, a.Tools.Root, files)
	var events []scopedEvent
	a.OnEvent = func(kind string, fields map[string]any) { events = append(events, scopedEvent{kind, fields}) }
	var out, errBuf bytes.Buffer
	a.Out, a.Err = &out, &errBuf
	if cfg != nil {
		cfg(a)
	}
	return a.Run(context.Background(), "task"), a, events, &out, &errBuf
}

func readCall(id, path string) model.ToolCall {
	b, _ := json.Marshal(map[string]string{"path": path})
	return call(id, "read_file", string(b))
}

func scopedBlock(scope string, size int, content string) string {
	return "[AGENTS.md for " + scope + " (" + formatInt(size) + " bytes): instructions for files under " + scope +
		"; they take precedence over broader project instructions there]\n" + content + "\n[end AGENTS.md for " + scope + "]"
}

func eventsOf(events []scopedEvent, kind string) []map[string]any {
	var out []map[string]any
	for _, e := range events {
		if e.kind == kind {
			out = append(out, e.fields)
		}
	}
	return out
}

const pkgA = "Run make test-a, not go test.\n"

func TestScopedAgentsMDOffByDefault(t *testing.T) {
	m := &scripted{replies: []model.Message{toolStep(readCall("1", "pkg/a/x.go")), {Role: "assistant", Content: "done"}}}
	a := newAgent(t, m)
	scopedFiles(t, a.Tools.Root, map[string]string{"pkg/a/x.go": "package a\n", "pkg/a/AGENTS.md": pkgA})
	plain := (&tools.Executor{Root: a.Tools.Root}).RunResult(context.Background(), readCall("1", "pkg/a/x.go")).Text
	var kinds []string
	a.OnEvent = func(kind string, _ map[string]any) { kinds = append(kinds, kind) }
	tr := a.Run(context.Background(), "task")
	if res := toolResults(tr.Messages); len(res) != 1 || res[0] != plain {
		t.Errorf("results = %q, want %q", res, plain)
	}
	if strings.Contains(tr.SystemPrompt, "make test-a") {
		t.Error("scoped file in the system prompt")
	}
	for _, req := range m.seen {
		for _, msg := range req {
			if strings.Contains(msg.Content, "make test-a") {
				t.Errorf("request carries the scoped file: %q", msg.Content)
			}
		}
	}
	b, _ := json.Marshal(tr)
	if strings.Contains(string(b), "scoped") {
		t.Errorf("trajectory JSON mentions scoped AGENTS.md: %s", b)
	}
	for _, k := range kinds {
		if strings.HasPrefix(k, "instructions.") {
			t.Errorf("event %s while off", k)
		}
	}
}

func TestScopedAgentsMDFirstAccessDeliversOnce(t *testing.T) {
	tr, _, events, out, _ := scopedRun(t, map[string]string{"pkg/a/x.go": "package a\n", "pkg/a/y.go": "package a // y\n", "pkg/a/AGENTS.md": pkgA},
		func(a *Agent) { a.Verbose = true },
		toolStep(readCall("1", "pkg/a/x.go")), toolStep(readCall("2", "pkg/a/y.go")))
	res := toolResults(tr.Messages)
	if len(res) != 2 {
		t.Fatalf("results = %q", res)
	}
	want := scopedBlock("pkg/a/", len(pkgA), strings.TrimRight(pkgA, "\n"))
	if !strings.HasSuffix(res[0], "\n\n"+want) || !strings.Contains(res[0], "package a") {
		t.Errorf("first result = %q, want it to end with %q", res[0], want)
	}
	if strings.Contains(res[1], "[AGENTS.md for") || !strings.Contains(res[1], "// y") {
		t.Errorf("second result = %q, want no second copy", res[1])
	}
	if !strings.Contains(out.String(), "loaded scoped AGENTS.md from pkg/a/AGENTS.md (30 bytes) via read_file\n") {
		t.Errorf("verbose output = %q", out.String())
	}
	loaded := eventsOf(events, "instructions.loaded")
	if len(loaded) != 1 {
		t.Fatalf("instructions.loaded = %v", loaded)
	}
	l := loaded[0]
	if l["path"] != "pkg/a/AGENTS.md" || l["scope_dir"] != "pkg/a/" || l["bytes"] != len(pkgA) || l["truncated"] != false ||
		l["tool"] != "read_file" || l["tool_call_id"] != "1" || l["step"] != 1 {
		t.Errorf("instructions.loaded = %v", l)
	}
	if !tr.ScopedAgentsMDEnabled || strings.Join(tr.ScopedAgentsMD, ",") != "pkg/a/AGENTS.md" || len(tr.ScopedAgentsMDLoads) != 1 ||
		tr.ScopedAgentsMDLoads[0].Message != 3 || tr.ScopedAgentsMDSince == "" {
		t.Errorf("trajectory: enabled %v, delivered %v, loads %+v, since %q", tr.ScopedAgentsMDEnabled, tr.ScopedAgentsMD, tr.ScopedAgentsMDLoads, tr.ScopedAgentsMDSince)
	}
	if tr.SystemPrompt != SystemPrompt(config.Default(), tr.Root, false) {
		t.Error("system prompt changed")
	}
}

func TestScopedAgentsMDNestingOrder(t *testing.T) {
	tr, _, _, _, _ := scopedRun(t, map[string]string{"pkg/a/x.go": "x", "pkg/AGENTS.md": "outer", "pkg/a/AGENTS.md": "inner"}, nil,
		toolStep(readCall("1", "pkg/a/x.go")))
	res := toolResults(tr.Messages)[0]
	outer, inner := strings.Index(res, "[AGENTS.md for pkg/ "), strings.Index(res, "[AGENTS.md for pkg/a/ ")
	if outer < 0 || inner < 0 || outer > inner {
		t.Errorf("result = %q, want pkg/ before pkg/a/", res)
	}
	if !strings.HasSuffix(res, scopedBlock("pkg/", 5, "outer")+"\n\n"+scopedBlock("pkg/a/", 5, "inner")) {
		t.Errorf("result = %q", res)
	}
}

func TestScopedAgentsMDNoOverlapWithStartup(t *testing.T) {
	tr, _, events, _, _ := scopedRun(t, map[string]string{"AGENTS.md": "root rules", "top.go": "t", "pkg/x.go": "x"}, nil,
		toolStep(readCall("1", "top.go"), readCall("2", "pkg/x.go"), call("3", "list_files", `{}`)))
	if !strings.Contains(tr.SystemPrompt, "root rules") {
		t.Error("startup AGENTS.md missing from the system prompt")
	}
	for _, r := range toolResults(tr.Messages) {
		if strings.Contains(r, "[AGENTS.md for") || strings.Contains(r, "root rules") {
			t.Errorf("result carries the startup file: %q", r)
		}
	}
	if len(eventsOf(events, "instructions.loaded")) != 0 {
		t.Error("instructions.loaded emitted")
	}
}

func TestScopedAgentsMDDirectoryTools(t *testing.T) {
	files := map[string]string{"pkg/a/x.go": "needle a", "pkg/a/AGENTS.md": "rules a", "pkg/b/z.go": "needle b", "pkg/b/AGENTS.md": "rules b"}
	for _, c := range []model.ToolCall{
		call("1", "grep", `{"pattern":"needle","path":"pkg/a"}`),
		call("1", "list_files", `{"path":"pkg/a"}`),
	} {
		tr, _, _, _, _ := scopedRun(t, files, nil, toolStep(c))
		if res := toolResults(tr.Messages)[0]; !strings.HasSuffix(res, scopedBlock("pkg/a/", 7, "rules a")) {
			t.Errorf("%s: result = %q", c.Function.Name, res)
		}
	}
	tr, _, _, _, _ := scopedRun(t, files, nil, toolStep(call("1", "grep", `{"pattern":"needle"}`)))
	if res := toolResults(tr.Messages)[0]; !strings.Contains(res, "pkg/b/z.go") || strings.Contains(res, "[AGENTS.md for") {
		t.Errorf("grep from .: result = %q, want matches and no scoped file", res)
	}
}

func TestScopedAgentsMDEdits(t *testing.T) {
	files := map[string]string{"pkg/a/x.go": "old\n", "pkg/a/AGENTS.md": "rules a", "pkg/b/AGENTS.md": "rules b", "pkg/c/AGENTS.md": "rules c", "pkg/c/c.go": "v1\n"}
	patch := "*** Begin Patch\n*** Update File: pkg/a/x.go\n@@\n-old\n+new\n*** Add File: pkg/b/y.go\n+package b\n*** End Patch"
	b, _ := json.Marshal(map[string]string{"input": patch})
	tr, _, _, _, _ := scopedRun(t, files, func(a *Agent) { a.Tools.ApplyPatch = true },
		toolStep(call("1", tools.ApplyPatchToolName, string(b))),
		toolStep(call("2", "write_file", `{"path":"pkg/c/new.go","content":"n"}`)),
		toolStep(call("3", "edit_file", `{"path":"pkg/c/c.go","old_str":"v1","new_str":"v2"}`)))
	res := toolResults(tr.Messages)
	if !strings.HasSuffix(res[0], scopedBlock("pkg/a/", 7, "rules a")+"\n\n"+scopedBlock("pkg/b/", 7, "rules b")) {
		t.Errorf("apply_patch result = %q", res[0])
	}
	if !strings.HasSuffix(res[1], scopedBlock("pkg/c/", 7, "rules c")) || strings.Contains(res[2], "[AGENTS.md for") {
		t.Errorf("write_file / edit_file results = %q", res[1:])
	}
}

func TestScopedAgentsMDFailuresDoNotTrigger(t *testing.T) {
	files := map[string]string{"pkg/a/AGENTS.md": "rules a", "pkg/a/secret.txt": "s", "pkg/a/x.go": "x"}
	tr, _, events, _, _ := scopedRun(t, files, func(a *Agent) {
		fa, err := tools.NewFileAccess(a.Tools.Root, []string{"pkg/a/secret.txt"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.Tools.FileAccess = fa
		withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "read_file", "exit2", "no")}})
	}, toolStep(readCall("1", "pkg/a/x.go")))
	if res := toolResults(tr.Messages)[0]; strings.Contains(res, "[AGENTS.md for") || !strings.Contains(res, "denied by pre_tool hook") {
		t.Errorf("hook-denied result = %q", res)
	}
	tr2, _, events2, _, _ := scopedRun(t, files, func(a *Agent) {
		fa, err := tools.NewFileAccess(a.Tools.Root, []string{"pkg/a/secret.txt"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.Tools.FileAccess = fa
	}, toolStep(readCall("1", "pkg/a/missing.go"), readCall("2", "../outside.go"), readCall("3", "pkg/a/secret.txt"), call("4", "list_files", `{"path":"pkg/a/nope"}`)))
	for i, res := range toolResults(tr2.Messages) {
		if strings.Contains(res, "[AGENTS.md for") || !strings.HasPrefix(res, "error:") {
			t.Errorf("failed call %d result = %q", i+1, res)
		}
	}
	if len(eventsOf(append(events, events2...), "instructions.loaded")) != 0 || len(tr.ScopedAgentsMDLoads)+len(tr2.ScopedAgentsMDLoads) != 0 {
		t.Error("a failed call delivered a scoped file")
	}
}

func TestScopedAgentsMDFileAccess(t *testing.T) {
	tr, _, events, out, _ := scopedRun(t, map[string]string{"pkg/a/AGENTS.md": "rules a", "pkg/a/x.go": "x"}, func(a *Agent) {
		fa, err := tools.NewFileAccess(a.Tools.Root, []string{"pkg/a/AGENTS.md"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.Tools.FileAccess = fa
		a.Verbose = true
	}, toolStep(readCall("1", "pkg/a/x.go")), toolStep(readCall("2", "pkg/a/x.go")))
	if res := toolResults(tr.Messages)[0]; strings.Contains(res, "[AGENTS.md for") || strings.Contains(res, "rules a") {
		t.Errorf("result = %q", res)
	}
	skipped := eventsOf(events, "instructions.skipped")
	if len(skipped) != 1 || skipped[0]["reason"] != "file_access" || skipped[0]["path"] != "pkg/a/AGENTS.md" {
		t.Errorf("instructions.skipped = %v, want one with reason file_access", skipped)
	}
	if !strings.Contains(out.String(), "skipped scoped AGENTS.md pkg/a/AGENTS.md (file_access)") {
		t.Errorf("verbose output = %q", out.String())
	}
}

func TestScopedAgentsMDSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(outside, []byte("outside rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, _, events, _, _ := scopedRun(t, map[string]string{"pkg/a/x.go": "x", "pkg/b/AGENTS.md": "rules b"}, func(a *Agent) {
		if err := os.Symlink(outside, filepath.Join(a.Tools.Root, "pkg", "a", "AGENTS.md")); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		// An in-tree symlink is fine.
		if err := os.Symlink(filepath.Join(a.Tools.Root, "pkg", "b", "AGENTS.md"), filepath.Join(a.Tools.Root, "pkg", "c", "AGENTS.md")); err != nil {
			if err := os.MkdirAll(filepath.Join(a.Tools.Root, "pkg", "c"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(a.Tools.Root, "pkg", "b", "AGENTS.md"), filepath.Join(a.Tools.Root, "pkg", "c", "AGENTS.md")); err != nil {
				t.Fatal(err)
			}
		}
	}, toolStep(readCall("1", "pkg/a/x.go"), call("2", "list_files", `{"path":"pkg/c"}`)))
	res := toolResults(tr.Messages)
	if strings.Contains(res[0], "outside rules") || strings.Contains(res[0], "[AGENTS.md for") {
		t.Errorf("escaping symlink delivered: %q", res[0])
	}
	if !strings.HasSuffix(res[1], scopedBlock("pkg/c/", 7, "rules b")) {
		t.Errorf("in-tree symlink result = %q", res[1])
	}
	if s := eventsOf(events, "instructions.skipped"); len(s) != 1 || s[0]["reason"] != "outside_root" {
		t.Errorf("instructions.skipped = %v", s)
	}
}

func TestScopedAgentsMDCaps(t *testing.T) {
	big := strings.Repeat("é", 20<<10) // 40 KiB of 2-byte runes
	tr, _, _, _, _ := scopedRun(t, map[string]string{"pkg/a/AGENTS.md": "x" + big, "pkg/a/x.go": "x"}, nil, toolStep(readCall("1", "pkg/a/x.go")))
	res := toolResults(tr.Messages)[0]
	start := strings.Index(res, "]\n") + 2
	end := strings.LastIndex(res, "\n[end AGENTS.md for pkg/a/]")
	body := res[start:end]
	if !strings.HasSuffix(body, "\n[AGENTS.md truncated]") || !utf8.ValidString(body) {
		t.Fatalf("block not truncated on a rune boundary: ...%q", body[max(0, len(body)-60):])
	}
	if n := len(strings.TrimSuffix(body, "\n[AGENTS.md truncated]")); n > maxAgentsMD || n < maxAgentsMD-1 {
		t.Errorf("kept %d bytes, want ~%d", n, maxAgentsMD)
	}
	if l := tr.ScopedAgentsMDLoads[0]; !l.Truncated || l.Bytes != 1+len(big) {
		t.Errorf("load = %+v", l)
	}

	part := strings.Repeat("r", 30<<10)
	tr, _, events, _, errBuf := scopedRun(t, map[string]string{"a/AGENTS.md": part, "b/AGENTS.md": part, "c/AGENTS.md": part, "d/AGENTS.md": "small", "a/x": "x", "b/x": "x", "c/x": "x", "d/x": "x"}, nil,
		toolStep(readCall("1", "a/x"), readCall("2", "b/x"), readCall("3", "c/x"), readCall("4", "d/x")))
	res2 := toolResults(tr.Messages)
	if !strings.Contains(res2[0], "[AGENTS.md for a/") || !strings.Contains(res2[1], "[AGENTS.md for b/") || !strings.Contains(res2[3], "[AGENTS.md for d/") {
		t.Errorf("files within the cap not delivered")
	}
	notice := "[scoped AGENTS.md c/AGENTS.md not delivered: the per-run limit of 65,536 bytes of scoped instructions is reached]"
	if !strings.HasSuffix(res2[2], "\n\n"+notice) || strings.Contains(res2[2], "[AGENTS.md for") {
		t.Errorf("over-cap result = %q", res2[2][max(0, len(res2[2])-200):])
	}
	skipped := eventsOf(events, "instructions.skipped")
	if len(skipped) != 1 || skipped[0]["reason"] != "cap" || skipped[0]["path"] != "c/AGENTS.md" {
		t.Errorf("instructions.skipped = %v", skipped)
	}
	if !strings.Contains(errBuf.String(), "warning: scoped AGENTS.md c/AGENTS.md not delivered") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestScopedAgentsMDTruncationAndHooks(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "hook.json")
	long := strings.Repeat("line of code\n", 200)
	tr, a, _, _, _ := scopedRun(t, map[string]string{"pkg/a/AGENTS.md": pkgA, "pkg/a/x.go": long}, func(a *Agent) {
		a.Tools.MaxOutputChars = 300
		withHooks(t, a, config.Hooks{PostTool: []config.Hook{
			hookstest.Hook(t, "read_file", "stdin", stdin),
		}})
	}, toolStep(readCall("1", "pkg/a/x.go")))
	_ = a
	res := toolResults(tr.Messages)[0]
	want := scopedBlock("pkg/a/", len(pkgA), strings.TrimRight(pkgA, "\n"))
	if !strings.HasSuffix(res, "\n\n"+want) {
		t.Errorf("block cut by truncation: %q", res)
	}
	if !strings.Contains(res, "characters elided]") {
		t.Errorf("tool output not truncated: %q", res)
	}
	b, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(b, &in); err != nil || !strings.HasSuffix(in.Output, want) {
		t.Errorf("post_tool hook input result lacks the block: %v %s", err, b)
	}
}

func TestScopedAgentsMDCompactionRedelivers(t *testing.T) {
	f := &fakeModel{replies: []reply{
		{msg: toolStep(readCall("c1", "pkg/a/x.go"))}, {msg: reads(2, 1)}, {msg: reads(3, 1)},
		{msg: reads(4, 1)}, {msg: reads(5, 1)}, {msg: reads(6, 1)}, {msg: reads(7, 1), input: 15000},
		{msg: toolStep(readCall("c8", "pkg/a/y.go"))},
		{msg: submit},
	}}
	a, _ := compactAgent(t, f, 20000)
	a.ScopedAgentsMD = true
	scopedFiles(t, a.Tools.Root, map[string]string{"pkg/a/AGENTS.md": pkgA, "pkg/a/x.go": "x", "pkg/a/y.go": "y"})
	tr := a.Run(context.Background(), "the task")
	if tr.ExitStatus != StatusSubmitted || len(tr.Compactions) != 1 || tr.Compactions[0].Stage != "elide" {
		t.Fatalf("status %s %q, compactions %+v", tr.ExitStatus, tr.Submission, tr.Compactions)
	}
	res := toolResults(tr.Messages)
	if !strings.Contains(res[0], "[AGENTS.md for pkg/a/") || !strings.Contains(res[7], "[AGENTS.md for pkg/a/") {
		t.Errorf("not re-delivered after elision: first %q, last %q", res[0], res[7])
	}
	if len(tr.ScopedAgentsMDLoads) != 2 || strings.Join(tr.ScopedAgentsMD, ",") != "pkg/a/AGENTS.md" {
		t.Errorf("loads %+v, delivered %v", tr.ScopedAgentsMDLoads, tr.ScopedAgentsMD)
	}

	first := reads(1, 1)
	first.ToolCalls = append(first.ToolCalls, readCall("cx", "pkg/a/x.go"))
	f = &fakeModel{summary: "## Task\nread big.txt", replies: []reply{
		{msg: first}, {msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000},
		{msg: toolStep(readCall("c4", "pkg/a/y.go"))},
		{msg: submit},
	}}
	a, _ = compactAgent(t, f, 30000)
	a.ScopedAgentsMD = true
	scopedFiles(t, a.Tools.Root, map[string]string{"pkg/a/AGENTS.md": pkgA, "pkg/a/x.go": "x", "pkg/a/y.go": "y"})
	tr = a.Run(context.Background(), "the task")
	if f.summaryRequests() == 0 {
		t.Fatalf("no summary; compactions %+v", tr.Compactions)
	}
	var last *model.Message
	for i := range tr.Messages {
		if tr.Messages[i].ToolCallID == "c4" {
			last = &tr.Messages[i]
		}
	}
	if last == nil || !strings.Contains(last.Content, "[AGENTS.md for pkg/a/") {
		t.Errorf("not re-delivered after summary: %+v (status %s %q, compactions %+v)", last, tr.ExitStatus, tr.Submission, tr.Compactions)
	}
}

func TestScopedAgentsMDSubagent(t *testing.T) {
	a, _, cm := subagentAgent(t,
		[]model.Message{toolStep(readCall("p1", "pkg/a/x.go")), assistantCalls(delegateCall("p2", "look at pkg/a")), {Role: "assistant", Content: "done"}},
		[]model.Message{toolStep(readCall("k1", "pkg/a/y.go")), {Role: "assistant", Content: "found it" + submitMarker}})
	a.SkipAgentsMD = false
	a.ScopedAgentsMD = true
	scopedFiles(t, a.Tools.Root, map[string]string{"pkg/a/AGENTS.md": pkgA, "pkg/a/x.go": "x", "pkg/a/y.go": "y"})
	tr := a.Run(context.Background(), "task")
	if !strings.Contains(toolResults(tr.Messages)[0], "[AGENTS.md for pkg/a/") {
		t.Errorf("parent result = %q", toolResults(tr.Messages)[0])
	}
	if len(cm.seen) < 2 {
		t.Fatalf("child requests = %d", len(cm.seen))
	}
	child := toolResults(cm.seen[1])
	if len(child) != 1 || !strings.HasSuffix(child[0], scopedBlock("pkg/a/", len(pkgA), strings.TrimRight(pkgA, "\n"))) {
		t.Errorf("child result = %q", child)
	}
}

func TestScopedAgentsMDResume(t *testing.T) {
	tr, a, _, _, _ := scopedRun(t, map[string]string{"pkg/a/AGENTS.md": pkgA, "pkg/a/x.go": "x", "pkg/a/y.go": "y"}, nil,
		toolStep(readCall("1", "pkg/a/x.go")))
	if len(tr.ScopedAgentsMDLoads) != 1 {
		t.Fatalf("loads = %+v", tr.ScopedAgentsMDLoads)
	}
	b, _ := json.Marshal(tr)
	clone := func() *trajectory.Trajectory {
		var c trajectory.Trajectory
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		return &c
	}
	if got := ScopedAgentsMDDelivered(clone()); strings.Join(got, ",") != "pkg/a/AGENTS.md" {
		t.Errorf("delivered = %v", got)
	}

	off := &scripted{}
	ra := &Agent{Model: off, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: config.Default(), Resume: clone(), FollowUp: "more"}
	if got := ra.Run(context.Background(), ""); got.ExitStatus != StatusError || !strings.Contains(got.Submission, "scoped_agents_md setting differs") || len(off.seen) != 0 {
		t.Errorf("resume with the option off: %s %q, %d requests", got.ExitStatus, got.Submission, len(off.seen))
	}

	on := &scripted{replies: []model.Message{toolStep(readCall("2", "pkg/a/y.go")), {Role: "assistant", Content: "done"}}}
	ra = &Agent{Model: on, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: config.Default(), Resume: clone(), FollowUp: "more", ScopedAgentsMD: true}
	got := ra.Run(context.Background(), "")
	res := toolResults(got.Messages)
	if last := res[len(res)-1]; strings.Contains(last, "[AGENTS.md for") {
		t.Errorf("re-sent on resume while still in context: %q", last)
	}
	if !got.ScopedAgentsMDEnabled || len(got.ScopedAgentsMDLoads) != 1 {
		t.Errorf("resumed trajectory: enabled %v, loads %+v", got.ScopedAgentsMDEnabled, got.ScopedAgentsMDLoads)
	}
}

func TestScopedAgentsMDReadErrorsAndModified(t *testing.T) {
	tr, _, events, _, errBuf := scopedRun(t, map[string]string{"bad/AGENTS.md": "\xff\xfe", "empty/AGENTS.md": "", "bad/x": "x", "empty/x": "x", "new/x": "x"}, nil,
		toolStep(readCall("1", "bad/x"), readCall("2", "empty/x")),
		toolStep(call("3", "write_file", `{"path":"new/AGENTS.md","content":"model-written rules"}`)),
		toolStep(readCall("4", "new/x")))
	for _, r := range toolResults(tr.Messages) {
		if strings.Contains(r, "[AGENTS.md for") || strings.Contains(r, "model-written") {
			t.Errorf("result = %q", r)
		}
	}
	for _, w := range []string{"warning: reading AGENTS.md: bad/AGENTS.md: file is not valid UTF-8", "warning: reading AGENTS.md: empty/AGENTS.md: file is empty"} {
		if !strings.Contains(errBuf.String(), w) {
			t.Errorf("stderr lacks %q:\n%s", w, errBuf.String())
		}
	}
	reasons := map[string]string{}
	for _, s := range eventsOf(events, "instructions.skipped") {
		reasons[s["path"].(string)] = s["reason"].(string)
	}
	if reasons["bad/AGENTS.md"] != "read_error" || reasons["empty/AGENTS.md"] != "read_error" || reasons["new/AGENTS.md"] != "modified" {
		t.Errorf("skip reasons = %v", reasons)
	}
	if tr.ExitStatus != StatusCompleted {
		t.Errorf("status = %s", tr.ExitStatus)
	}
}

func TestScopedAgentsMDReadOnlyAndNoAgentsMD(t *testing.T) {
	files := map[string]string{"pkg/a/AGENTS.md": pkgA, "pkg/a/x.go": "x"}
	tr, _, _, _, _ := scopedRun(t, files, func(a *Agent) { a.Tools.ReadOnly = true }, toolStep(readCall("1", "pkg/a/x.go")))
	if !strings.Contains(toolResults(tr.Messages)[0], "[AGENTS.md for pkg/a/") {
		t.Error("read-only run did not deliver")
	}
	tr, _, _, _, _ = scopedRun(t, files, func(a *Agent) { a.SkipAgentsMD = true }, toolStep(readCall("1", "pkg/a/x.go")))
	if strings.Contains(toolResults(tr.Messages)[0], "[AGENTS.md for") || tr.ScopedAgentsMDEnabled {
		t.Error("SkipAgentsMD did not turn scoped delivery off")
	}
}
