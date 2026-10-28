package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscoverPluginSkills(t *testing.T) {
	_, cwd := repo(t)
	root := pluginRoot(t)
	write(t, filepath.Join(root, "skills/a/SKILL.md"), skillMD("a", "Skill a.", "body a"))
	write(t, filepath.Join(root, "skills/a/notes.txt"), "notes")
	write(t, filepath.Join(root, "skills/b/SKILL.md"), "---\nname: b\n---\nno description\n")
	write(t, filepath.Join(root, "skills/c/nested/SKILL.md"), skillMD("c", "Nested.", ""))
	outside := t.TempDir()
	write(t, filepath.Join(outside, "evil/SKILL.md"), skillMD("evil", "Outside.", ""))
	if err := os.Symlink(filepath.Join(outside, "evil"), filepath.Join(root, "skills/evil")); err != nil {
		t.Skip(err)
	}
	set, warnings := DiscoverWith(cwd, "", []PluginSource{{Plugin: "kit", Root: root, Dir: filepath.Join(root, "skills")}})
	if got := strings.Join(set.Names(), ","); got != "a" {
		t.Fatalf("names %q, warnings %q", got, warnings)
	}
	k, _ := set.Get("a")
	if k.Scope != ScopePlugin || k.Plugin != "kit" {
		t.Errorf("skill %+v", k)
	}
	if countContaining(warnings, "skipping "+filepath.Join(root, "skills/b")) != 1 || countContaining(warnings, "evil") != 1 || countContaining(warnings, "outside the plugin root") != 1 {
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
	user := t.TempDir()
	write(t, filepath.Join(user, "review/SKILL.md"), skillMD("review", "User review.", ""))
	p1, p2 := pluginRoot(t), pluginRoot(t)
	write(t, filepath.Join(p1, "skills/review/SKILL.md"), skillMD("review", "Plugin review.", ""))
	write(t, filepath.Join(p1, "skills/lint/SKILL.md"), skillMD("lint", "First lint.", ""))
	write(t, filepath.Join(p2, "skills/lint/SKILL.md"), skillMD("lint", "Second lint.", ""))
	set, warnings := DiscoverWith(cwd, user, []PluginSource{
		{Plugin: "one", Root: p1, Dir: filepath.Join(p1, "skills")},
		{Plugin: "two", Root: p2, Dir: filepath.Join(p2, "skills")},
	})
	if k, _ := set.Get("review"); k.Scope != ScopeUser {
		t.Errorf("review from %+v", k)
	}
	if k, _ := set.Get("lint"); k.Plugin != "one" {
		t.Errorf("lint from %+v", k)
	}
	if countContaining(warnings, "shadowed by "+filepath.Join(user, "review")) != 1 || countContaining(warnings, "shadowed by "+filepath.Join(p1, "skills/lint")) != 1 {
		t.Errorf("warnings %q", warnings)
	}
}

func TestDiscoverPluginSkillsDirProblems(t *testing.T) {
	_, cwd := repo(t)
	root := pluginRoot(t)
	write(t, filepath.Join(root, "skills"), "not a dir")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "x/SKILL.md"), skillMD("x", "X.", ""))
	root2 := pluginRoot(t)
	if err := os.Symlink(outside, filepath.Join(root2, "skills")); err != nil {
		t.Skip(err)
	}
	set, warnings := DiscoverWith(cwd, "", []PluginSource{
		{Plugin: "file", Root: root, Dir: filepath.Join(root, "skills")},
		{Plugin: "link", Root: root2, Dir: filepath.Join(root2, "skills")},
		{Plugin: "none", Root: root, Dir: filepath.Join(root, "missing")},
	})
	if set.Len() != 0 || countContaining(warnings, "plugin file") != 1 || countContaining(warnings, "plugin link") != 1 || len(warnings) != 2 {
		t.Errorf("set %v, warnings %q", set.Names(), warnings)
	}
}
