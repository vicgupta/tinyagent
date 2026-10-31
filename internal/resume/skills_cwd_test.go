package resume

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vicgupta/tinyagent/internal/skills"
)

func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name, "SKILL.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Resume's own skills discovery reads only ./skills in the root, the same
// as a fresh run: not ./.agents/skills, a parent or the home folder.
func TestDiscoverSkillsCwdOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "home-skill")
	parent := t.TempDir()
	writeSkill(t, filepath.Join(parent, ".agents", "skills"), "parent-skill")
	root := filepath.Join(parent, "repo")
	writeSkill(t, filepath.Join(root, "skills"), "local")
	writeSkill(t, filepath.Join(root, ".agents", "skills"), "legacy")

	got := discoverSkills(root).Names()
	if !slices.Equal(got, []string{"local"}) {
		t.Errorf("resume discovery = %v, want [local]", got)
	}
	fresh, _ := skills.Discover(root)
	if !slices.Equal(got, fresh.Names()) {
		t.Errorf("resume %v != fresh run %v", got, fresh.Names())
	}
}
