package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
)

var noticeRe = regexp.MustCompile(`\[full output: ([\d,]+) bytes, ([\d,]+) lines, saved to (\S+); use read_file with offset/limit or grep on that path\]$`)

func outputExecutor(t *testing.T, max int) *Executor {
	t.Helper()
	root := t.TempDir()
	o, err := NewOutputFiles(root, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &Executor{Root: root, MaxOutputChars: max, BashTimeout: 30 * time.Second, OutputFiles: o}
}

func toolCall(id, name string, args map[string]any) model.ToolCall {
	b, _ := json.Marshal(args)
	return model.ToolCall{ID: id, Function: model.FunctionCall{Name: name, Arguments: string(b)}}
}

func TestOutputFilesSaveBashOutput(t *testing.T) { // AC2, AC3
	e := outputExecutor(t, 1000)
	r := e.RunResult(context.Background(), toolCall("call_1", "bash", map[string]any{"command": "seq 1 5000"}))
	if len(r.Text) > 1000 {
		t.Errorf("result is %d bytes, want <= 1000", len(r.Text))
	}
	m := noticeRe.FindStringSubmatch(r.Text)
	if m == nil || strings.Count(r.Text, "[full output:") != 1 {
		t.Fatalf("no single notice in:\n%s", r.Text)
	}
	if !strings.HasPrefix(r.Text, "<returncode>0</returncode>\n<output>\n1\n2\n") || !strings.Contains(r.Text, "characters elided") || !strings.Contains(r.Text, "4999\n5000\n</output>") {
		t.Errorf("preview lacks head/tail:\n%s", r.Text)
	}
	path := m[3]
	if filepath.IsAbs(path) || !strings.HasPrefix(path, ".tinyagent/tool-output/"+e.OutputFiles.RunID+"/") || path != r.OutputFile {
		t.Errorf("path %q (OutputFile %q)", path, r.OutputFile)
	}
	full := filepath.Join(e.Root, path)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if m[1] != groupDigits(len(data)) || r.OutputBytes != len(data) || m[2] != groupDigits(countLines(string(data))) {
		t.Errorf("notice %q/%q, file %d bytes", m[1], m[2], len(data))
	}
	var want strings.Builder
	want.WriteString("<returncode>0</returncode>\n<output>\n")
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&want, "%d\n", i)
	}
	want.WriteString("</output>")
	if string(data) != want.String() {
		t.Errorf("saved file is not the full result text (%d bytes, want %d)", len(data), want.Len())
	}
	if fi, _ := os.Stat(full); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	// Lines 2502-2511 of the result text are the command's lines 2500-2509
	// (the result starts with two header lines).
	got := e.Run(context.Background(), toolCall("c2", "read_file", map[string]any{"path": path, "offset": 2502, "limit": 10}))
	if !strings.HasPrefix(got, "2500\n2501\n") || !strings.Contains(got, "2509") || strings.Contains(got, "2510\n") {
		t.Errorf("paged read = %q", got)
	}
	ents, _ := os.ReadDir(filepath.Dir(full))
	if len(ents) != 1 {
		t.Errorf("run dir holds %d entries, want 1 (no temp files left)", len(ents))
	}
}

func TestOutputFilesBoundWithMultibyte(t *testing.T) { // AC3
	for _, max := range []int{300, 301, 302, 333, 1000} {
		e := outputExecutor(t, max)
		r := e.limit(model.ToolCall{ID: "x", Function: model.FunctionCall{Name: "bash"}}, strings.Repeat("héllo wörld ✓\n", 500), false)
		if len(r.Text) > max {
			t.Errorf("max %d: %d bytes", max, len(r.Text))
		}
		if !strings.Contains(r.Text, "saved to") || !json.Valid([]byte(fmt.Sprintf("%q", r.Text))) || !strings.Contains(r.Text, "elided") {
			t.Errorf("max %d: %q", max, r.Text)
		}
		for i, c := range r.Text {
			if c == 0xFFFD {
				t.Errorf("max %d: split rune at %d", max, i)
				break
			}
		}
	}
}

func TestOutputFilesUnderLimitAndOffUnchanged(t *testing.T) { // AC1
	e := outputExecutor(t, 1000)
	small := e.RunResult(context.Background(), toolCall("a", "bash", map[string]any{"command": "echo hi"}))
	if small.OutputFile != "" || small.Notice != "" {
		t.Errorf("under-limit result saved: %+v", small)
	}
	off := &Executor{Root: t.TempDir(), MaxOutputChars: 1000, BashTimeout: 30 * time.Second}
	r := off.RunResult(context.Background(), toolCall("a", "bash", map[string]any{"command": "seq 1 5000"}))
	raw, _ := off.run(context.Background(), "bash", `{"command":"seq 1 5000"}`, nil)
	if r.Text != Truncate(raw, 1000) || r.OutputFile != "" {
		t.Errorf("feature off changed the result")
	}
	if _, err := os.Stat(filepath.Join(off.Root, ".tinyagent")); !os.IsNotExist(err) {
		t.Errorf(".tinyagent created with the feature off: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Root, ".tinyagent")); !os.IsNotExist(err) {
		t.Errorf("directory created before any save: %v", err)
	}
}

func TestOutputFilesReadFileNotSaved(t *testing.T) { // AC5
	e := outputExecutor(t, 500)
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(filepath.Join(e.Root, "big.txt"), []byte(b.String()), 0o644)
	r := e.RunResult(context.Background(), toolCall("r", "read_file", map[string]any{"path": "big.txt"}))
	if r.OutputFile != "" || len(r.Text) > 500 {
		t.Errorf("read_file saved or too long: %+v", r)
	}
	if !strings.HasSuffix(r.Text, "[file has 300 lines; output was truncated, use read_file on big.txt with offset/limit to read the rest]") {
		t.Errorf("notice missing:\n%s", r.Text)
	}
	if _, err := os.Stat(e.OutputFiles.Dir); !os.IsNotExist(err) {
		t.Errorf("output dir exists after read_file: %v", err)
	}
}

func TestOutputFilesExcludedFromWalks(t *testing.T) { // AC6
	e := outputExecutor(t, 100000)
	path, err := e.OutputFiles.save("c1", "only-in-saved-file\n")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.Root, "a.txt"), []byte("other\n"), 0o644)
	if out := e.Run(context.Background(), toolCall("g", "grep", map[string]any{"pattern": "only-in-saved-file", "path": "."})); strings.Contains(out, "tool-output") {
		t.Errorf("grep . matched the saved file: %q", out)
	}
	if out := e.Run(context.Background(), toolCall("g", "grep", map[string]any{"pattern": "only-in-saved-file", "path": ".tinyagent"})); strings.Contains(out, "tool-output") {
		t.Errorf("grep .tinyagent matched the saved file: %q", out)
	}
	if out := e.Run(context.Background(), toolCall("g", "grep", map[string]any{"pattern": "only-in-saved-file", "path": ".tinyagent/tool-output"})); !strings.Contains(out, path) {
		t.Errorf("grep on the output dir = %q, want a match in %s", out, path)
	}
	if out := e.Run(context.Background(), toolCall("l", "list_files", map[string]any{"pattern": "*.txt"})); strings.Contains(out, "tool-output") || !strings.Contains(out, "a.txt") {
		t.Errorf("list_files = %q", out)
	}
	if out := e.Run(context.Background(), toolCall("l", "list_files", map[string]any{"path": ".tinyagent/tool-output", "pattern": "*.txt"})); !strings.Contains(out, path) {
		t.Errorf("list_files in the output dir = %q", out)
	}
	// With the feature off, nothing extra is skipped.
	off := &Executor{Root: e.Root}
	if out := off.Run(context.Background(), toolCall("g", "grep", map[string]any{"pattern": "only-in-saved-file", "path": "."})); !strings.Contains(out, path) {
		t.Errorf("feature off: grep = %q", out)
	}
}

func TestOutputFilesSearchOfOutputDirNotSaved(t *testing.T) {
	e := outputExecutor(t, 300)
	if _, err := e.OutputFiles.save("c1", strings.Repeat("needle\n", 200)); err != nil {
		t.Fatal(err)
	}
	r := e.RunResult(context.Background(), toolCall("g", "grep", map[string]any{"pattern": "needle", "path": ".tinyagent/tool-output"}))
	if r.OutputFile != "" || strings.Contains(r.Text, "[full output:") {
		t.Errorf("grep of the output dir was saved: %+v", r)
	}
}

func TestOutputFilesReadOnlyGrep(t *testing.T) { // AC7
	e := outputExecutor(t, 300)
	e.ReadOnly = true
	os.WriteFile(filepath.Join(e.Root, "src.txt"), []byte(strings.Repeat("match here\n", 200)), 0o644)
	r := e.RunResult(context.Background(), toolCall("g1", "grep", map[string]any{"pattern": "match"}))
	if r.OutputFile == "" {
		t.Fatalf("grep result not saved: %q", r.Text)
	}
	got := e.Run(context.Background(), toolCall("r", "read_file", map[string]any{"path": r.OutputFile, "limit": 2}))
	if !strings.Contains(got, "src.txt:1: match here") {
		t.Errorf("read_file of saved grep = %q", got)
	}
}

func TestOutputFilesMCPResultSaved(t *testing.T) { // AC4
	e := outputExecutor(t, 500)
	text := strings.Repeat("mcp line\n", 400)
	e.ExternalCall = func(_ context.Context, name, _ string) (string, bool, bool) {
		return text, false, name == "srv__fetch"
	}
	r := e.RunResult(context.Background(), toolCall("m1", "srv__fetch", nil))
	if r.OutputFile == "" || len(r.Text) > 500 || !noticeRe.MatchString(r.Text) {
		t.Fatalf("MCP result not saved: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(e.Root, r.OutputFile)); string(b) != text {
		t.Errorf("saved MCP text differs")
	}
}

func TestOutputFilesWriteFailureFallsBack(t *testing.T) { // AC8
	e := outputExecutor(t, 500)
	// A regular file where the directory must go fails even for root.
	os.WriteFile(filepath.Join(e.Root, ".tinyagent"), []byte("x"), 0o644)
	var warnings []string
	e.OutputFiles.Warn = func(s string) { warnings = append(warnings, s) }
	raw, _ := e.run(context.Background(), "bash", `{"command":"seq 1 3000"}`, nil)
	for i := 0; i < 2; i++ {
		r := e.RunResult(context.Background(), toolCall(fmt.Sprint("c", i), "bash", map[string]any{"command": "seq 1 3000"}))
		if r.Text != Truncate(raw, 500) || r.OutputFile != "" || r.IsError {
			t.Errorf("fallback result differs: %+v", r)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tool_output_files") {
		t.Errorf("warnings = %q, want exactly one", warnings)
	}
}

func TestOutputFilesCallIDCollision(t *testing.T) {
	e := outputExecutor(t, 100)
	a, err1 := e.OutputFiles.save("../x/y", "one")
	b, err2 := e.OutputFiles.save("../x/y", "two")
	if err1 != nil || err2 != nil || a == b || !strings.HasSuffix(a, "/___x_y.txt") || !strings.HasSuffix(b, "/___x_y-2.txt") {
		t.Errorf("paths %q %q (%v %v)", a, b, err1, err2)
	}
}

func TestResolveOutputDir(t *testing.T) { // AC9
	root := t.TempDir()
	for _, d := range []string{"../outside", "/elsewhere", ".", "a/../.."} {
		if _, err := ResolveOutputDir(root, d); err == nil || !strings.Contains(err.Error(), "tool_output_files.dir") {
			t.Errorf("%q: err = %v", d, err)
		}
	}
	if got, err := ResolveOutputDir(root, ""); err != nil || got != filepath.Join(root, ".tinyagent", "tool-output") {
		t.Errorf("default = %q, %v", got, err)
	}
	if got, err := ResolveOutputDir(root, filepath.Join(root, "out")); err != nil || got != filepath.Join(root, "out") {
		t.Errorf("absolute inside = %q, %v", got, err)
	}
}

func TestOutputFilesPrune(t *testing.T) { // AC10
	root := t.TempDir()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var earlier []*OutputFiles
	for i := 0; i < 3; i++ {
		o, err := NewOutputFiles(root, "", base.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.save("c", "x"); err != nil {
			t.Fatal(err)
		}
		earlier = append(earlier, o)
	}
	dir := earlier[0].Dir
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o644)
	os.Mkdir(filepath.Join(dir, "other"), 0o755)
	fourth, _ := NewOutputFiles(root, "", base.Add(time.Hour))
	if err := fourth.Prune(2); err != nil {
		t.Fatal(err)
	}
	var names []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		names = append(names, e.Name())
	}
	want := []string{earlier[1].RunID, earlier[2].RunID, "notes.txt", "other"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("after prune: %v, want %v", names, want)
	}
	if err := fourth.Prune(0); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 4 {
		t.Errorf("max_runs 0 removed entries")
	}
	if err := (&OutputFiles{Dir: filepath.Join(root, "missing")}).Prune(1); err != nil {
		t.Errorf("missing dir: %v", err)
	}
}

func TestRunIDFormat(t *testing.T) {
	o, err := NewOutputFiles(t.TempDir(), "", time.Date(2026, 10, 25, 14, 3, 7, 123e6, time.FixedZone("x", -4*3600)))
	if err != nil {
		t.Fatal(err)
	}
	if !runIDPattern.MatchString(o.RunID) || !strings.HasPrefix(o.RunID, "20261025T180307.123Z-") {
		t.Errorf("run id %q", o.RunID)
	}
}

func TestTruncateWithNotice(t *testing.T) {
	if _, ok := TruncateWithNotice("abc", "a long notice", 5); ok {
		t.Error("notice longer than max fit")
	}
	if got, ok := TruncateWithNotice(strings.Repeat("x", 100), "[n]", 4); !ok || got != "[n]" {
		t.Errorf("tiny max = %q %v", got, ok)
	}
	if got, _ := TruncateWithNotice("short", "[n]", 100); got != "short\n[n]" {
		t.Errorf("fits = %q", got)
	}
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 184213: "184,213", 1234567: "1,234,567"} {
		if got := groupDigits(n); got != want {
			t.Errorf("groupDigits(%d) = %q", n, got)
		}
	}
}
