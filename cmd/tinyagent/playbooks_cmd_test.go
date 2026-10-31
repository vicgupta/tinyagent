package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/playbooks"
)

const pbBugFix = `---
name: bug-fix
intent: Fix a crash bug.
match: [crash, bug, fix]
skills: [go-testing]
---

1. Reproduce the crash.
2. Fix it.

## Done when

The test passes.
$ true
`

const pbDocs = `---
name: docs
intent: Write documentation pages.
---

1. Write the page.

## Done

The page exists.
`

const pbSkill = "---\nname: go-testing\ndescription: Go tests\n---\nUse table tests.\n"

// pbRepo writes a cwd with two playbooks, one skill and an empty config.
func pbRepo(t *testing.T, withSkill bool) (dir, cfg string) {
	t.Helper()
	dir = t.TempDir()
	os.MkdirAll(filepath.Join(dir, "playbooks"), 0o755)
	os.WriteFile(filepath.Join(dir, "playbooks", "bug-fix.md"), []byte(pbBugFix), 0o644)
	os.WriteFile(filepath.Join(dir, "playbooks", "docs.md"), []byte(pbDocs), 0o644)
	if withSkill {
		os.MkdirAll(filepath.Join(dir, "skills", "go-testing"), 0o755)
		os.WriteFile(filepath.Join(dir, "skills", "go-testing", "SKILL.md"), []byte(pbSkill), 0o644)
	}
	cfg = filepath.Join(dir, "c.yaml")
	os.WriteFile(cfg, nil, 0o644)
	return dir, cfg
}

// runPB runs one task against a fake model and returns the request bodies.
func runPB(t *testing.T, dir, cfg, task string, args ...string) (code int, stdout, stderr string, bodies []map[string]any) {
	t.Helper()
	srv := fakeOpenAIBodies(t, &bodies)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			var flagErr string
			code, _, flagErr = runWith(t, append([]string{"-t", task, "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg}, args...)...)
			os.Stderr.WriteString(flagErr)
		})
	})
	return
}

func system(b map[string]any) string {
	msgs, _ := b["messages"].([]any)
	var s strings.Builder
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
			c, _ := json.Marshal(mm["content"])
			s.Write(c)
		}
	}
	return s.String()
}

func fakePicker(t *testing.T, answer string, err error, calls *int) {
	t.Helper()
	old := playbookPicker
	playbookPicker = func(config.AgentConfig, string) (playbooks.Picker, error) {
		return func(context.Context, string) (string, error) { *calls++; return answer, err }, nil
	}
	t.Cleanup(func() { playbookPicker = old })
}

func TestPlaybooksOffByDefault(t *testing.T) {
	dir, cfg := pbRepo(t, true)
	calls := 0
	fakePicker(t, "bug-fix", nil, &calls)
	code, _, stderr, bodies := runPB(t, dir, cfg, "fix the crash bug")
	if code != 0 || strings.Contains(stderr, "playbook:") || calls != 0 {
		t.Fatalf("exit %d, picker calls %d, stderr:\n%s", code, calls, stderr)
	}
	if strings.Contains(system(bodies[0]), "Playbook") {
		t.Error("playbook in the prompt while off")
	}
}

func TestPlaybookKeywordsLineEventAndRecord(t *testing.T) {
	dir, cfg := pbRepo(t, true)
	calls := 0
	fakePicker(t, "docs", nil, &calls)
	out := filepath.Join(dir, "run.json")
	code, stdout, stderr, bodies := runPB(t, dir, cfg, "fix the crash bug in the parser", "--playbooks", "--json", "-o", out)
	if code != 0 || calls != 0 {
		t.Fatalf("exit %d, picker calls %d; stderr:\n%s", code, calls, stderr)
	}
	if !strings.Contains(stderr, `playbook: bug-fix (keywords: `) {
		t.Errorf("stderr line missing:\n%s", stderr)
	}
	if !strings.Contains(stdout, `"playbook.selected"`) || !strings.Contains(stdout, `"method":"keywords"`) {
		t.Errorf("event missing:\n%s", stdout)
	}
	sys := system(bodies[0])
	for _, want := range []string{"Playbook: bug-fix", "Reproduce the crash", "Use table tests"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	data, _ := os.ReadFile(out)
	var tr struct {
		Playbook struct{ Name, Method, Hash string } `json:"playbook"`
	}
	json.Unmarshal(data, &tr)
	if tr.Playbook.Name != "bug-fix" || tr.Playbook.Method != "keywords" || tr.Playbook.Hash == "" {
		t.Errorf("-o record = %+v", tr.Playbook)
	}
}

func TestPlaybookModelFallback(t *testing.T) {
	dir, cfg := pbRepo(t, true)
	calls := 0
	fakePicker(t, "docs", nil, &calls)
	code, stdout, stderr, _ := runPB(t, dir, cfg, "something unrelated entirely", "--playbooks", "--json")
	if code != 0 || calls != 1 || !strings.Contains(stderr, "playbook: docs (model fallback)") || !strings.Contains(stdout, `"method":"model"`) {
		t.Fatalf("exit %d calls %d; stderr:\n%s", code, calls, stderr)
	}
	for _, c := range []struct {
		name string
		args []string
		cfg  string
	}{
		{"flag", []string{"--no-playbook-fallback"}, ""},
		{"config", nil, "playbook_fallback: false\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls = 0
			os.WriteFile(cfg, []byte(c.cfg), 0o644)
			code, _, stderr, _ := runPB(t, dir, cfg, "something unrelated entirely", append([]string{"--playbooks"}, c.args...)...)
			if code != 0 || calls != 0 || !strings.Contains(stderr, "playbook: none") {
				t.Errorf("exit %d calls %d; stderr:\n%s", code, calls, stderr)
			}
		})
	}
	os.WriteFile(cfg, nil, 0o644)
	calls = 0
	fakePicker(t, "", errors.New("down"), &calls)
	code, _, stderr, _ = runPB(t, dir, cfg, "something unrelated entirely", "--playbooks")
	if code != 0 || !strings.Contains(stderr, "playbook: none") {
		t.Errorf("model error: exit %d; stderr:\n%s", code, stderr)
	}
}

func TestPlaybookMissingSkillWarns(t *testing.T) {
	dir, cfg := pbRepo(t, false)
	code, _, stderr, _ := runPB(t, dir, cfg, "x", "--playbook", "bug-fix")
	if code != 0 || !strings.Contains(stderr, "go-testing") || !strings.Contains(stderr, "playbook: bug-fix (--playbook)") {
		t.Errorf("exit %d; stderr:\n%s", code, stderr)
	}
}

func TestPlaybookHandoffNeedsTrust(t *testing.T) {
	dir, cfg := pbRepo(t, true)
	os.WriteFile(filepath.Join(dir, "playbooks", "wf.md"), []byte("---\nname: wf\nintent: hand off\nworkflow: wf.yaml\n---\n\n1. go\n\n## Done\n\nok\n"), 0o644)
	code, _, stderr, bodies := runPB(t, dir, cfg, "x", "--playbook", "wf")
	if code != 2 || !strings.Contains(stderr, "--trust-config") || len(bodies) != 0 {
		t.Errorf("exit %d, %d requests; stderr:\n%s", code, len(bodies), stderr)
	}
}

func bothMatch(t *testing.T) (dir, cfg string) {
	dir, cfg = pbRepo(t, true)
	os.MkdirAll(filepath.Join(dir, "wskills"), 0o755)
	os.WriteFile(filepath.Join(dir, "wskills", "plan-tasks.md"), []byte(wfSkill), 0o644)
	os.WriteFile(filepath.Join(dir, "wf.yaml"), []byte("version: 1\nskills_dir: wskills\nworkflows:\n  fixit:\n    intent: fix a crash bug\n    stages:\n      - skill: plan-tasks\n"), 0o644)
	return
}

func TestPlaybookAndWorkflowBothMatch(t *testing.T) {
	dir, cfg := bothMatch(t)
	wf := filepath.Join(dir, "wf.yaml")
	oldI, oldA := playbookInteractive, playbookAsk
	t.Cleanup(func() { playbookInteractive, playbookAsk = oldI, oldA })

	playbookInteractive = func(*options) bool { return false }
	code, _, stderr, bodies := runPB(t, dir, cfg, "fix the crash bug", "--playbooks", "--workflow", wf)
	if code != 2 || len(bodies) != 0 || !strings.Contains(stderr, "--playbook bug-fix or --workflow-name fixit") {
		t.Fatalf("non-interactive: exit %d, %d requests; stderr:\n%s", code, len(bodies), stderr)
	}

	playbookInteractive = func(*options) bool { return true }
	asked := 0
	playbookAsk = func(_ io.Reader, _ io.Writer, pb, w string) (string, error) {
		asked++
		if pb != "bug-fix" || w != "fixit" {
			t.Errorf("asked about %s/%s", pb, w)
		}
		return "p", nil
	}
	code, _, stderr, bodies = runPB(t, dir, cfg, "fix the crash bug", "--playbooks", "--workflow", wf)
	if code != 0 || asked != 1 || len(bodies) == 0 || !strings.Contains(system(bodies[0]), "Playbook: bug-fix") {
		t.Fatalf("answer p: exit %d asked %d; stderr:\n%s", code, asked, stderr)
	}
}

func runPBCmd(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			var h, f string
			code, h, f = runWith(t, append(args, "--cwd", dir)...)
			os.Stdout.WriteString(h)
			os.Stderr.WriteString(f)
		})
	})
	return
}

func TestPlaybooksSubcommand(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runWith(t, "playbooks")
	if code != 0 || !strings.Contains(out, "install") || !strings.Contains(out, "validate") {
		t.Errorf("bare playbooks: exit %d\n%s", code, out)
	}
	code, out, errOut := runPBCmd(t, dir, "playbooks", "install", "samples")
	if code != 0 || strings.Count(out, "wrote ") != 6 {
		t.Fatalf("install: exit %d\n%s%s", code, out, errOut)
	}
	code, out, _ = runPBCmd(t, dir, "playbooks", "install", "samples")
	if code != 0 || strings.Count(out, "kept ") != 6 {
		t.Errorf("reinstall keeps files: exit %d\n%s", code, out)
	}
	code, out, _ = runPBCmd(t, dir, "playbooks", "install", "samples", "--force")
	if code != 0 || strings.Count(out, "wrote ") != 6 {
		t.Errorf("--force: exit %d\n%s", code, out)
	}
	code, out, _ = runPBCmd(t, dir, "playbooks", "list")
	for _, n := range playbooks.SampleNames() {
		if !strings.Contains(out, n) {
			t.Errorf("list lacks %s:\n%s", n, out)
		}
	}
	if code != 0 {
		t.Errorf("list exit %d", code)
	}
	code, out, _ = runPBCmd(t, dir, "playbooks", "validate", "playbooks/bug-fix.md")
	if code != 0 || !strings.Contains(out, "ok") {
		t.Errorf("validate: exit %d\n%s", code, out)
	}
	os.WriteFile(filepath.Join(dir, "playbooks", "bad.md"), []byte("no frontmatter\n"), 0o644)
	if code, _, _ = runPBCmd(t, dir, "playbooks", "validate", "playbooks/bad.md"); code != 1 {
		t.Errorf("invalid: exit %d, want 1", code)
	}
	outside := filepath.Join(t.TempDir(), "x.md")
	os.WriteFile(outside, []byte(pbDocs), 0o644)
	if code, _, _ = runPBCmd(t, dir, "playbooks", "validate", outside); code != 2 {
		t.Errorf("outside --cwd: exit %d, want 2", code)
	}
}

func TestPlaybooksIgnoreHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, "playbooks"), 0o755)
	os.WriteFile(filepath.Join(home, "playbooks", "docs.md"), []byte(pbDocs), 0o644)
	dir := t.TempDir()
	code, out, _ := runPBCmd(t, dir, "playbooks", "list")
	if code != 0 || strings.Contains(out, "docs") {
		t.Errorf("read HOME: exit %d\n%s", code, out)
	}
}

func TestPlaybookResumeRefusesChanged(t *testing.T) {
	dir, cfg := pbRepo(t, true)
	out := filepath.Join(dir, "run.json")
	if code, _, stderr, _ := runPB(t, dir, cfg, "x", "--playbook", "bug-fix", "-o", out); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	os.WriteFile(filepath.Join(dir, "playbooks", "bug-fix.md"), []byte(strings.Replace(pbBugFix, "Fix it.", "Fix it well.", 1)), 0o644)
	var bodies []map[string]any
	srv := fakeOpenAIBodies(t, &bodies)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	var code int
	stderr := captureStderr(t, func() {
		var f string
		code, _, f = runWith(t, "--resume", out, "--cwd", dir, "-c", cfg, "--no-stream", "-t", "continue")
		os.Stderr.WriteString(f)
	})
	if code == 0 || !strings.Contains(stderr, "changed since this checkpoint") || len(bodies) != 0 {
		t.Errorf("exit %d, %d requests; stderr:\n%s", code, len(bodies), stderr)
	}
}

func TestPlaybooksCheckRow(t *testing.T) {
	dir, cfg := pbRepo(t, true)
	t.Setenv("OPENAI_API_KEY", "fake")
	_, out, errOut := runPBCmd(t, dir, "--check", "--playbooks", "-c", cfg, "-m", "openai/gpt-4o")
	if !strings.Contains(out+errOut, "playbooks") || !strings.Contains(out+errOut, "bug-fix") {
		t.Errorf("--check lacks the playbooks row:\n%s%s", out, errOut)
	}
}

func TestChatPlaybookCommand(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t)
	os.MkdirAll(filepath.Join(root, "playbooks"), 0o755)
	os.WriteFile(filepath.Join(root, "playbooks", "docs.md"), []byte(pbDocs), 0o644)
	in := "/help\n/playbook nope\n/playbook docs\nfirst\nsecond\n/quit\n"
	r := runChatTest(t, root, strings.NewReader(in), nil, "-m", "openai/gpt-4o")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	for _, want := range []string{"/playbook", `no playbook "nope"`, "the next message follows playbook docs"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
		}
	}
	reqs := p.requests()
	if len(reqs) < 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	if !strings.Contains(lastUser(t, reqs[0]), "Write the page") {
		t.Errorf("first turn lacks the playbook: %q", lastUser(t, reqs[0]))
	}
	if strings.Contains(lastUser(t, reqs[len(reqs)-1]), "Write the page") {
		t.Error("playbook leaked into the second turn")
	}
}
