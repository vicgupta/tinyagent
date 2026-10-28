package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func plugin(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"$schema":"`+SchemaV1+`","name":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad(t *testing.T) {
	ok, bad := plugin(t, "demo"), plugin(t, "Bad--Name")
	ls, err := Load([]config.Plugin{{Path: ok}, {Path: bad}})
	if err != nil || len(ls) != 2 || ls[0].Err != nil || ls[1].Err == nil {
		t.Fatalf("%+v, %v", ls, err)
	}
	src := SkillSources(ls)
	if len(src) != 1 || src[0].Plugin != "demo" || filepath.Base(src[0].Dir) != SkillsDir {
		t.Errorf("sources %+v", src)
	}
	if _, err := Load([]config.Plugin{{Path: ok}, {Path: plugin(t, "demo")}}); err == nil || !strings.Contains(err.Error(), `"demo"`) {
		t.Errorf("duplicate names: %v", err)
	}
}
