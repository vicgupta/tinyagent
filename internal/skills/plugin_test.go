package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var pluginN int

// pluginRoot returns a fresh plugin root inside cwd: plugin skills are read
// only from inside the working directory.
func pluginRoot(t *testing.T, cwd string) string {
	t.Helper()
	pluginN++
	dir := filepath.Join(cwd, "plugins", fmt.Sprint(pluginN))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscoverPluginSkills(t *testing.T) {
	_, cwd := repo(t)
	root := pluginRoot(t, cwd)
	write(t, filepath.Join(root, "skills/a/SKILL.md"), skillMD("a", "Skill a.", "body a"))
	write(t, filepath.Join(root, "skills/a/notes.txt"), "notes")
	write(t, filepath.Join(root, "skills/b/SKILL.md"), "---\nname: b\n---\nno description\n")
	write(t, filepath.Join(root, "skills/c/nested/SKILL.md"), skillMD("c", "Nested.", ""))
	outside := t.TempDir()
	write(t, filepath.Join(outside, "evil/SKILL.md"), skillMD("evil", "Outside.", ""))
	if err := os.Symlink(filepath.Join(outside, "evil"), filepath.Join(root, "skills/evil")); err != nil {
		t.Skip(err)
	}
	set, warnings := DiscoverWith(cwd, []PluginSource{{Plugin: "kit", Root: root, Dir: filepath.Join(root, "skills")}})
	if got := strings.Join(set.Names(), ","); got != "a" {
		t.Fatalf("names %q, warnings %q", got, warnings)
	}
	k, _ := set.Get("a")
	if k.Scope != ScopePlugin || k.Plugin != "kit" {
		t.Errorf("skill %+v", k)
	}
	if countContaining(warnings, "skipping "+filepath.Join(root, "skills/b")) != 1 || countContaining(warnings, "evil") != 1 || countContaining(warnings, "outside the working directory") != 1 {
		t.Errorf("warnings %q", warnings)
	}
	if got, err := set.Load("a", "notes.txt"); err != nil || got != "notes" {
		t.Errorf("bundled file %q, %v", got, err)
	}
	if got, err := set.Load("a", ""); err != nil || !strings.Contains(got, "body a") {
		t.Errorf("load %q, %v", got, err)
	}
}

func TestDiscoverPluginPrecedence(t *testing.T) {
	_, cwd := repo(t)
	write(t, filepath.Join(cwd, "skills/review/SKILL.md"), skillMD("review", "Project review.", ""))
	p1, p2 := pluginRoot(t, cwd), pluginRoot(t, cwd)
	write(t, filepath.Join(p1, "skills/review/SKILL.md"), skillMD("review", "Plugin review.", ""))
	write(t, filepath.Join(p1, "skills/lint/SKILL.md"), skillMD("lint", "First lint.", ""))
	write(t, filepath.Join(p2, "skills/lint/SKILL.md"), skillMD("lint", "Second lint.", ""))
	set, warnings := DiscoverWith(cwd, []PluginSource{
		{Plugin: "one", Root: p1, Dir: filepath.Join(p1, "skills")},
		{Plugin: "two", Root: p2, Dir: filepath.Join(p2, "skills")},
	})
	if k, _ := set.Get("review"); k.Scope != ScopeProject {
		t.Errorf("review from %+v", k)
	}
	if k, _ := set.Get("lint"); k.Plugin != "one" {
		t.Errorf("lint from %+v", k)
	}
	if countContaining(warnings, "shadowed by "+filepath.Join(cwd, "skills/review")) != 1 || countContaining(warnings, "shadowed by "+filepath.Join(p1, "skills/lint")) != 1 {
		t.Errorf("warnings %q", warnings)
	}
}

func TestDiscoverPluginOutsideCwdNotRead(t *testing.T) {
	_, cwd := repo(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(outside, "skills/far/SKILL.md"), skillMD("far", "Far.", ""))
	set, warnings := DiscoverWith(cwd, []PluginSource{{Plugin: "far", Root: outside, Dir: filepath.Join(outside, "skills")}})
	if set.Len() != 0 || countContaining(warnings, "outside the working directory") != 1 {
		t.Errorf("set %v, warnings %q", set.Names(), warnings)
	}
}

func TestDiscoverPluginSkillsDirProblems(t *testing.T) {
	_, cwd := repo(t)
	root := pluginRoot(t, cwd)
	write(t, filepath.Join(root, "skills"), "not a dir")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "x/SKILL.md"), skillMD("x", "X.", ""))
	root2 := pluginRoot(t, cwd)
	if err := os.Symlink(outside, filepath.Join(root2, "skills")); err != nil {
		t.Skip(err)
	}
	set, warnings := DiscoverWith(cwd, []PluginSource{
		{Plugin: "file", Root: root, Dir: filepath.Join(root, "skills")},
		{Plugin: "link", Root: root2, Dir: filepath.Join(root2, "skills")},
		{Plugin: "none", Root: root, Dir: filepath.Join(root, "missing")},
	})
	if set.Len() != 0 || countContaining(warnings, "plugin file") != 1 || countContaining(warnings, "plugin link") != 1 || len(warnings) != 2 {
		t.Errorf("set %v, warnings %q", set.Names(), warnings)
	}
}
