package harness

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var reGuideFile = regexp.MustCompile("(?s)<!-- file: (\\S+) -->\n(`{3,})[a-z]*\n(.*?)\n`{3,}\n")

// TestWorkflowGuideExamples keeps docs/workflow-guide.md honest: every
// example workflow loads and the example behavior file parses.
func TestWorkflowGuideExamples(t *testing.T) {
	data, err := os.ReadFile("../../docs/workflow-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var workflows, behaviors []string
	for _, m := range reGuideFile.FindAllStringSubmatch(string(data), -1) {
		p := filepath.Join(dir, filepath.FromSlash(m[1]))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(m[3]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasSuffix(p, "workflow.yaml"):
			workflows = append(workflows, p)
		case strings.HasSuffix(p, "behavior.yaml"):
			behaviors = append(behaviors, p)
		}
	}
	if len(workflows) != 3 || len(behaviors) != 1 {
		t.Fatalf("found %d workflows and %d behavior files, want 3 and 1", len(workflows), len(behaviors))
	}
	for _, p := range workflows {
		if _, err := Load(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range behaviors {
		b, _ := os.ReadFile(p)
		if _, err := parseBehavior(b); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
