package playbooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

const good = `---
name: bug-fix
intent: Fix a bug by reproducing it first.
match: [repro, crash]
skills: [go-test]
---
Intro text.

1. Reproduce it.
2. Fix it
   with care.
- [ ] Run the tests.

## Done when

The test passes.
$ go test ./...
`

func TestParse(t *testing.T) {
	p, err := Parse("x", "bug-fix", []byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 3 || p.Steps[1] != "Fix it with care." || p.Steps[2] != "Run the tests." {
		t.Errorf("steps %q", p.Steps)
	}
	if !strings.HasPrefix(p.Done, "The test passes.") || len(p.Checks) != 1 || p.Checks[0] != "go test ./..." {
		t.Errorf("done %q checks %q", p.Done, p.Checks)
	}
	if len(p.Match) != 2 || len(p.Skills) != 1 {
		t.Errorf("match %q skills %q", p.Match, p.Skills)
	}
}

func TestParseValidation(t *testing.T) {
	for name, tc := range map[string]struct{ data, want string }{
		"no frontmatter": {"1. a\n", "frontmatter"},
		"unclosed":       {"---\nname: a\n", "not closed"},
		"no name":        {"---\nintent: x\n---\n1. a\n## Done\nok\n", "name is required"},
		"bad name":       {"---\nname: Bad_Name\nintent: x\n---\n1. a\n## Done\nok\n", "lowercase"},
		"mismatch":       {"---\nname: other\nintent: x\n---\n1. a\n## Done\nok\n", "doesn't match"},
		"no intent":      {"---\nname: a\n---\n1. a\n## Done\nok\n", "intent is required"},
		"unknown key":    {"---\nname: a\nintent: x\nfoo: 1\n---\n1. a\n## Done\nok\n", "foo"},
		"no steps":       {"---\nname: a\nintent: x\n---\ntext\n## Done\nok\n", "no steps"},
		"no done":        {"---\nname: a\nintent: x\n---\n1. a\n", "no done"},
		"skill path":     {"---\nname: a\nintent: x\nskills: [../x]\n---\n1. a\n## Done\nok\n", "skill name"},
		"abs workflow":   {"---\nname: a\nintent: x\nworkflow: /etc/w.yaml\n---\n1. a\n## Done\nok\n", "inside the working directory"},
		"dotdot wf":      {"---\nname: a\nintent: x\nworkflow: ../w.yaml\n---\n1. a\n## Done\nok\n", "inside the working directory"},
	} {
		if _, err := Parse("x", "a", []byte(tc.data)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", name, err, tc.want)
		}
	}
}

const pstackFix = `### Fixing a broken build

Use this when CI is red and someone wants it green again.
Look at the failure before touching code.

1. Read the failing job's log with ` + "`ci-logs`" + `.
2. Reproduce locally.
   Keep notes.
3. Fix and push, then use **verify** to confirm.

**Reply:** what failed, why, and the fix.
`

func pstackCheckout(t *testing.T, root string) string {
	base := filepath.Join(root, "playbooks", "plugins", "pstack")
	write(t, filepath.Join(base, "skills", "poteto-mode", "playbooks", "fix-build.md"), pstackFix)
	write(t, filepath.Join(base, "skills", "poteto-mode", "playbooks", "broken.md"), "no heading here\n")
	write(t, filepath.Join(base, "skills", "ci-logs", "SKILL.md"), "---\nname: ci-logs\n---\nRead CI logs.\n")
	return base
}

func TestParsePstackAndDiscover(t *testing.T) {
	root := t.TempDir()
	base := pstackCheckout(t, root)
	pbs, warns := Discover(root)
	if len(pbs) != 1 {
		t.Fatalf("got %v, warns %q", Names(pbs), warns)
	}
	p := pbs[0]
	if p.Name != "fix-build" || p.Format != FormatPstack || len(p.Steps) != 3 || p.Steps[1] != "Reproduce locally. Keep notes." {
		t.Errorf("%+v", p)
	}
	if !strings.HasPrefix(p.Intent, "Fixing a broken build. Use this when CI is red") {
		t.Errorf("intent %q", p.Intent)
	}
	if p.Done != "what failed, why, and the fix." || strings.Join(p.Skills, ",") != "ci-logs" {
		t.Errorf("done %q skills %q", p.Done, p.Skills)
	}
	if p.SkillsDir != filepath.Join(base, "skills") {
		t.Errorf("skills dir %s", p.SkillsDir)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "broken.md") || !strings.Contains(warns[0], "no title heading") {
		t.Errorf("warns %q", warns)
	}
	sk, w := p.LoadSkills(root)
	if len(sk) != 1 || sk[0].Body != "Read CI logs." || len(w) != 0 {
		t.Errorf("skills %+v warns %q", sk, w)
	}
}

func TestDiscoverLayoutsAndClash(t *testing.T) {
	root := t.TempDir()
	pstackCheckout(t, root)
	write(t, filepath.Join(root, "playbooks", "bug-fix.md"), good)
	write(t, filepath.Join(root, "playbooks", "fix-build", "PLAYBOOK.md"), strings.Replace(good, "name: bug-fix", "name: fix-build", 1))
	write(t, filepath.Join(root, "playbooks", "README.md"), "# not a playbook")
	write(t, filepath.Join(root, "playbooks", "bad.md"), "---\nname: bad\n---\n")
	write(t, filepath.Join(root, "playbooks", "junk", "x.txt"), "x")
	pbs, warns := Discover(root)
	if got := strings.Join(Names(pbs), ","); got != "bug-fix,fix-build" {
		t.Fatalf("names %s warns %q", got, warns)
	}
	if fb, _ := Get(pbs, "fix-build"); fb.Format != FormatTinyAgent {
		t.Errorf("user-written should beat pstack: %+v", fb)
	}
	all := strings.Join(warns, "\n")
	for _, want := range []string{"bad.md", "unknown layout", "shadowed", "broken.md"} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings missing %q:\n%s", want, all)
		}
	}
}

func TestDiscoverCwdOnly(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	t.Setenv("HOME", outside)
	write(t, filepath.Join(outside, "playbooks", "bug-fix.md"), good)
	write(t, filepath.Join(root, ".agents", "playbooks", "bug-fix.md"), good)
	write(t, filepath.Join(outside, "evil.md"), strings.Replace(good, "bug-fix", "evil", 1))
	write(t, filepath.Join(outside, "skills", "go-test", "SKILL.md"), "secret")
	if pbs, _ := Discover(root); len(pbs) != 0 {
		t.Fatalf("read outside ./playbooks: %v", Names(pbs))
	}
	if err := os.MkdirAll(filepath.Join(root, "playbooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "evil.md"), filepath.Join(root, "playbooks", "evil.md")); err != nil {
		t.Skip(err)
	}
	write(t, filepath.Join(root, "playbooks", "bug-fix.md"), good)
	if err := os.Symlink(filepath.Join(outside, "skills"), filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	pbs, warns := Discover(root)
	if strings.Join(Names(pbs), ",") != "bug-fix" || !strings.Contains(strings.Join(warns, "\n"), "evil.md") {
		t.Fatalf("names %v warns %q", Names(pbs), warns)
	}
	sk, w := pbs[0].LoadSkills(root)
	if len(sk) != 0 || len(w) != 1 {
		t.Errorf("symlinked skills dir escaped: %+v %q", sk, w)
	}
}

func TestLoadSkillsMissingAndHash(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "playbooks", "bug-fix.md"), strings.Replace(good, "[go-test]", "[go-test, nope]", 1))
	write(t, filepath.Join(root, "skills", "go-test", "SKILL.md"), "---\nname: go-test\n---\nRun go test.\n")
	pbs, _ := Discover(root)
	sk, w := pbs[0].LoadSkills(root)
	if len(sk) != 1 || len(w) != 1 || !strings.Contains(w[0], `skill "nope" not found`) {
		t.Fatalf("skills %+v warns %q", sk, w)
	}
	h1 := Hash(pbs[0], sk)
	sec := Section(pbs[0], sk)
	for _, want := range []string{"## Playbook: bug-fix", "- [ ] 1. Reproduce it.", "Done when:", "### Skill go-test", "Run go test."} {
		if !strings.Contains(sec, want) {
			t.Errorf("section missing %q:\n%s", want, sec)
		}
	}
	write(t, filepath.Join(root, "skills", "go-test", "SKILL.md"), "changed")
	pbs, _ = Discover(root)
	sk, _ = pbs[0].LoadSkills(root)
	if Hash(pbs[0], sk) == h1 {
		t.Error("hash ignores skill changes")
	}
}

func pb(name, intent string, match ...string) *Playbook {
	return &Playbook{Name: name, Intent: intent, Match: match}
}

func TestSelect(t *testing.T) {
	pbs := []*Playbook{pb("bug-fix", "fix a bug", "repro", "crash"), pb("feature", "build a new feature", "implement")}
	ctx := context.Background()
	called := 0
	pick := func(answer string, err error) Picker {
		return func(context.Context, string) (string, error) { called++; return answer, err }
	}
	s := Select(ctx, pbs, "repro the crash and fix it", pick("feature", nil))
	if s.Playbook == nil || s.Playbook.Name != "bug-fix" || s.Method != MethodKeywords || called != 0 {
		t.Fatalf("keywords: %+v", s)
	}
	if got := s.Line(); got != `playbook: bug-fix (keywords: "crash", "fix", "repro")` {
		t.Errorf("line %q", got)
	}
	// Miss: model picks.
	s = Select(ctx, pbs, "make the logo blue", pick(" Feature.\n", nil))
	if s.Playbook == nil || s.Playbook.Name != "feature" || s.Method != MethodModel || s.Line() != "playbook: feature (model fallback)" {
		t.Errorf("model: %+v", s)
	}
	// Tie: one word each.
	s = Select(ctx, pbs, "fix and implement", pick("none", nil))
	if s.Playbook != nil || !strings.Contains(s.Note, "chose none") || !strings.HasPrefix(s.Line(), "playbook: none (") {
		t.Errorf("none: %+v", s)
	}
	s = Select(ctx, pbs, "make the logo blue", pick("", errors.New("boom")))
	if s.Playbook != nil || !strings.Contains(s.Note, "boom") {
		t.Errorf("error: %+v", s)
	}
	s = Select(ctx, pbs, "make the logo blue", pick("deploy", nil))
	if s.Playbook != nil || !strings.Contains(s.Note, "not a playbook") {
		t.Errorf("unknown: %+v", s)
	}
	before := called
	s = Select(ctx, pbs, "make the logo blue", nil)
	if s.Playbook != nil || !strings.Contains(s.Note, "fallback is off") || called != before {
		t.Errorf("disabled: %+v", s)
	}
	if !strings.Contains(PickPrompt(pbs, "task x"), "- bug-fix: fix a bug") {
		t.Error("prompt")
	}
}

func TestSamplesValidAndInstall(t *testing.T) {
	want := "babysit-pr,bug-fix,feature,investigate,open-pr,refactor"
	if got := strings.Join(SampleNames(), ","); got != want {
		t.Fatalf("samples %s", got)
	}
	for n, data := range Samples() {
		p, err := Parse(n, strings.TrimSuffix(n, ".md"), data)
		if err != nil {
			t.Errorf("%s: %v", n, err)
			continue
		}
		if p.ReadOnly != (p.Name == "investigate") {
			t.Errorf("%s read_only %v", n, p.ReadOnly)
		}
	}
	root := t.TempDir()
	w, s, err := Install(root, false)
	if err != nil || len(w) != 6 || len(s) != 0 {
		t.Fatalf("fresh: %v %v %v", w, s, err)
	}
	path := filepath.Join(root, "playbooks", "bug-fix.md")
	write(t, path, "mine")
	w, s, err = Install(root, false)
	if err != nil || len(w) != 0 || len(s) != 6 {
		t.Fatalf("no-overwrite: %v %v %v", w, s, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "mine" {
		t.Error("overwrote without --force")
	}
	if w, _, err = Install(root, true); err != nil || len(w) != 6 {
		t.Fatalf("force: %v %v", w, err)
	}
	if b, _ := os.ReadFile(path); string(b) == "mine" {
		t.Error("--force didn't overwrite")
	}
	pbs, warns := Discover(root)
	if len(pbs) != 6 || len(warns) != 0 {
		t.Errorf("discover samples: %v %q", Names(pbs), warns)
	}
}

func TestDocsCopiesMatchSamples(t *testing.T) {
	for n, data := range Samples() {
		doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "playbooks", n))
		if err != nil || string(doc) != string(data) {
			t.Errorf("docs/playbooks/%s differs from the embedded sample (%v)", n, err)
		}
	}
}
