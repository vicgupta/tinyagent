package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillMD(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
}

// repo returns a temp repository (with .git) and a nested cwd inside it.
func repo(t *testing.T) (root, cwd string) {
	t.Helper()
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd = filepath.Join(root, "pkg", "a")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, cwd
}

func countContaining(ws []string, sub string) int {
	n := 0
	for _, w := range ws {
		if strings.Contains(w, sub) {
			n++
		}
	}
	return n
}

func TestDiscoverScansOnlyCwdSkills(t *testing.T) {
	root, cwd := repo(t)
	write(t, filepath.Join(cwd, "skills/release/SKILL.md"), skillMD("release", "Cut a release.", "Steps"))
	write(t, filepath.Join(cwd, "skills/notaskill/README.md"), "no SKILL.md")
	// Never scanned (#359): parents up to the repository root, .agents and
	// .claude skill folders, and the home folder.
	write(t, filepath.Join(root, "skills/up/SKILL.md"), skillMD("up", "Parent.", "x"))
	write(t, filepath.Join(root, ".agents/skills/agents-root/SKILL.md"), skillMD("agents-root", "no", "x"))
	write(t, filepath.Join(cwd, ".agents/skills/agents/SKILL.md"), skillMD("agents", "no", "x"))
	write(t, filepath.Join(cwd, ".claude/skills/claude/SKILL.md"), skillMD("claude", "no", "x"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	write(t, filepath.Join(home, ".agents/skills/home/SKILL.md"), skillMD("home", "no", "x"))
	write(t, filepath.Join(home, "skills/home2/SKILL.md"), skillMD("home2", "no", "x"))
	set, warns := Discover(cwd)
	if got := strings.Join(set.Names(), ","); got != "release" {
		t.Errorf("names = %s, warnings %v", got, warns)
	}
	k, _ := set.Get("release")
	if k.Scope != ScopeProject || k.Path != filepath.Join(cwd, "skills/release/SKILL.md") || k.Description != "Cut a release." {
		t.Errorf("release = %+v", k)
	}
	if countContaining(warns, ".agents/skills is not scanned") != 1 || countContaining(warns, ".claude/skills is not scanned") != 1 || len(warns) != 2 {
		t.Errorf("warnings = %q", warns)
	}
	if got := ProjectDirs(cwd); len(got) != 1 || got[0] != filepath.Join(cwd, "skills") {
		t.Errorf("ProjectDirs = %q", got)
	}
}

func TestDiscoverWithoutGitScansOnlyRoot(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "sub")
	write(t, filepath.Join(parent, "skills/up/SKILL.md"), skillMD("up", "d", "x"))
	write(t, filepath.Join(cwd, "skills/here/SKILL.md"), skillMD("here", "d", "x"))
	set, _ := Discover(cwd)
	if got := strings.Join(set.Names(), ","); got != "here" {
		t.Errorf("names = %s", got)
	}
}

func TestInvalidSkillsSkippedWithWarnings(t *testing.T) {
	_, cwd := repo(t)
	write(t, filepath.Join(cwd, "skills/empty/SKILL.md"), "---\nname: empty\ndescription: \"\"\n---\nbody")
	write(t, filepath.Join(cwd, "skills/broken/SKILL.md"), "---\nname: [unclosed\ndescription: x\n---\nbody")
	write(t, filepath.Join(cwd, "skills/ok/SKILL.md"), skillMD("ok", "fine", "b"))
	set, warns := Discover(cwd)
	if got := strings.Join(set.Names(), ","); got != "ok" {
		t.Errorf("names = %s", got)
	}
	if len(warns) != 2 || countContaining(warns, "empty/SKILL.md") != 1 || countContaining(warns, "broken/SKILL.md") != 1 {
		t.Errorf("warnings = %q", warns)
	}
}

func TestLenientNameValidation(t *testing.T) {
	_, cwd := repo(t)
	write(t, filepath.Join(cwd, "skills/dir-name/SKILL.md"), skillMD("Other_Name", "d", "b"))
	write(t, filepath.Join(cwd, "skills/fallback/SKILL.md"), "---\ndescription: no name\n---\nb")
	set, warns := Discover(cwd)
	if got := strings.Join(set.Names(), ","); got != "Other_Name,fallback" {
		t.Errorf("names = %s", got)
	}
	if len(warns) != 2 || countContaining(warns, "loaded anyway") != 2 {
		t.Errorf("warnings = %q", warns)
	}
}

func TestSymlinkedSkillDirAndSizeCap(t *testing.T) {
	_, cwd := repo(t)
	target := filepath.Join(t.TempDir(), "linked")
	write(t, filepath.Join(target, "SKILL.md"), skillMD("linked", "via symlink", "b"))
	if err := os.MkdirAll(filepath.Join(cwd, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cwd, "skills/linked")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	write(t, filepath.Join(cwd, "skills/huge/SKILL.md"), skillMD("huge", "big", strings.Repeat("x", MaxSkillFileBytes)))
	set, warns := Discover(cwd)
	if got := strings.Join(set.Names(), ","); got != "linked" {
		t.Errorf("names = %s", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "byte limit") {
		t.Errorf("warnings = %q", warns)
	}
}

func TestCatalogCap(t *testing.T) {
	_, cwd := repo(t)
	long := strings.Repeat("word ", 200)
	for i := range 80 {
		name := "s" + strings.Repeat("a", 3) + string(rune('a'+i/26)) + string(rune('a'+i%26))
		write(t, filepath.Join(cwd, "skills", name, "SKILL.md"), skillMD(name, long, "b"))
	}
	set, warns := Discover(cwd)
	c := set.Catalog()
	if len(c) > MaxCatalogChars || set.Omitted == 0 || set.Len()+set.Omitted != 80 {
		t.Errorf("catalog %d chars, %d skills, %d omitted", len(c), set.Len(), set.Omitted)
	}
	if !strings.Contains(c, "…") || countContaining(warns, "omitted") != 1 {
		t.Errorf("descriptions not shortened or no warning: %q", warns)
	}
}

func TestEmptySetHasNoCatalog(t *testing.T) {
	_, cwd := repo(t)
	set, warns := Discover(cwd)
	if set.Len() != 0 || set.Catalog() != "" || len(warns) != 0 {
		t.Errorf("set = %+v, warnings %q", set, warns)
	}
	var nilSet *Set
	if nilSet.Len() != 0 || nilSet.Catalog() != "" {
		t.Error("nil set not empty")
	}
}

func loaded(t *testing.T) (*Set, string) {
	t.Helper()
	_, cwd := repo(t)
	dir := filepath.Join(cwd, "skills/release")
	write(t, filepath.Join(dir, "SKILL.md"), skillMD("release", "Cut a release.", "\n# Release\n\nRun scripts/tag.sh.\n"))
	write(t, filepath.Join(dir, "scripts/tag.sh"), "#!/bin/sh\necho tag\n")
	write(t, filepath.Join(dir, "references/a.md"), "reference text")
	write(t, filepath.Join(dir, "assets/bin.dat"), "a\x00b")
	write(t, filepath.Join(cwd, "skills/other/SKILL.md"), skillMD("other", "secret", "secret body"))
	set, _ := Discover(cwd)
	return set, dir
}

func TestLoadInstructions(t *testing.T) {
	set, dir := loaded(t)
	out, err := set.Load("release", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<skill_content name="release">`, "# Release\n\nRun scripts/tag.sh.", "Skill directory: " + dir,
		"- scripts/tag.sh", "- references/a.md", "- assets/bin.dat", "</skill_content>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "description:") || strings.Contains(out, "- SKILL.md") {
		t.Errorf("frontmatter or SKILL.md leaked:\n%s", out)
	}
	if _, err := set.Load("nope", ""); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Errorf("unknown: %v", err)
	}
}

func TestLoadFileListCap(t *testing.T) {
	set, dir := loaded(t)
	for i := range MaxListedFiles + 5 {
		write(t, filepath.Join(dir, "references", fmt.Sprintf("f%03d.md", i)), "x")
	}
	out, _ := set.Load("release", "")
	if n := strings.Count(out, "\n- "); n != MaxListedFiles+1 || !strings.Contains(out, "more files not listed") {
		t.Errorf("%d list lines", n)
	}
}

func TestLoadBundledFile(t *testing.T) {
	set, _ := loaded(t)
	if out, err := set.Load("release", "references/a.md"); err != nil || out != "reference text" {
		t.Errorf("got %q, %v", out, err)
	}
	if _, err := set.Load("release", "assets/bin.dat"); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("binary: %v", err)
	}
	if _, err := set.Load("release", "missing.md"); err == nil {
		t.Error("missing file: no error")
	}
}

func TestLoadRefusesEscapes(t *testing.T) {
	set, dir := loaded(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	write(t, outside, "secret")
	for _, f := range []string{"../other/SKILL.md", "references/../../other/SKILL.md", outside, "/etc/passwd"} {
		if out, err := set.Load("release", f); err == nil || strings.Contains(out, "secret") {
			t.Errorf("file %q: out %q err %v", f, out, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"link.txt", "linkdir/secret.txt"} {
		if out, err := set.Load("release", f); err == nil || !strings.Contains(err.Error(), "outside") || out != "" {
			t.Errorf("symlink %q: out %q err %v", f, out, err)
		}
	}
}
