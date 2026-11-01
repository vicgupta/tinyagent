package playbooks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/harness"
	"github.com/vicgupta/tinyagent/internal/skills"
)

var (
	reGuideFile  = regexp.MustCompile("(?s)<!-- file: (\\S+) -->\n(`{3,})[a-z]*\n(.*?)\n`{3,}\n")
	reGuideRoute = regexp.MustCompile("(?s)<!-- route -->\n```text\n\\$ tinyagent [^\\n]*-t \"([^\"]+)\"\n(playbook: [^\\n]*)\n```")
)

// TestPlaybookGuideExamples keeps docs/playbook-guide.md honest: every
// example file is written to a temporary --cwd, every playbook must be
// discovered and parsed with no warnings, the skills it names must load
// and be valid Agent Skills, a workflow handoff target must load with the
// real workflow loader, and each documented routing line must be what the
// keyword router prints for that task.
func TestPlaybookGuideExamples(t *testing.T) {
	data, err := os.ReadFile("../../docs/playbook-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	nFiles := 0
	for _, m := range reGuideFile.FindAllStringSubmatch(string(data), -1) {
		p := filepath.Join(root, filepath.FromSlash(m[1]))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(m[3]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		nFiles++
	}
	if nFiles == 0 {
		t.Fatal("no <!-- file: --> blocks found")
	}

	pbs, warns := Discover(root)
	for _, w := range warns {
		t.Errorf("discover: %s", w)
	}
	if got := strings.Join(Names(pbs), ","); got != "add-endpoint,fix-lint,release" {
		t.Fatalf("playbooks = %s, want add-endpoint,fix-lint,release", got)
	}
	handoffs := 0
	for _, p := range pbs {
		if len(p.Checks) == 0 {
			t.Errorf("%s: no $ done-when commands", p.Name)
		}
		_, w := p.LoadSkills(root)
		for _, w := range w {
			t.Errorf("%s: %s", p.Name, w)
		}
		if p.Workflow != "" {
			handoffs++
			if _, err := harness.Load(filepath.Join(root, filepath.FromSlash(p.Workflow))); err != nil {
				t.Errorf("%s: workflow %s: %v", p.Name, p.Workflow, err)
			}
		}
	}
	if handoffs != 1 {
		t.Errorf("%d playbooks hand off to a workflow, want 1", handoffs)
	}
	if p, _ := Get(pbs, "add-endpoint"); p == nil || len(p.Skills) != 2 {
		t.Errorf("add-endpoint should name two skills")
	}

	set, sw := skills.Discover(root)
	for _, w := range sw {
		t.Errorf("skills: %s", w)
	}
	if set == nil {
		t.Error("skills.Discover found no skills")
	}

	routes := reGuideRoute.FindAllStringSubmatch(string(data), -1)
	if len(routes) != 3 {
		t.Fatalf("found %d <!-- route --> blocks, want 3", len(routes))
	}
	for _, r := range routes {
		sel := Select(t.Context(), pbs, r[1], nil)
		if got := sel.Line(); got != r[2] {
			t.Errorf("task %q routes to\n  %s\nguide says\n  %s", r[1], got, r[2])
		}
	}
}
