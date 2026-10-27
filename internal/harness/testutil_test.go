package harness

import (
	"os"
	"path/filepath"
	"testing"
)

const planSkill = "# Skill: plan-tasks\n> Intent: Break a spec into small, ordered tasks before writing any code.\n\n**Inputs**\n- spec\n- repo_path\n\n**Success check:** every task has acceptance criteria and a verification step, in dependency order.\n"

const testSkill = "# Skill: run-tests\n> Intent: Run the Go test suite and report failures.\n\n**Inputs**\n- previous_output\n\n**Success check:** `go test -count=1 ./...` passes.\n"

// writeFiles writes name -> content under dir and returns dir.
func writeFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
