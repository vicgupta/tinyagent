package tinyagent_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// makeRecipe returns the recipe lines of target in the Makefile, with the
// leading tab and any @ silencing prefix removed.
func makeRecipe(t *testing.T, makefile, target string) []string {
	t.Helper()
	var lines []string
	in := false
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, target+":") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if !strings.HasPrefix(line, "\t") {
			break
		}
		lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "\t"), "@"))
	}
	if !in {
		t.Fatalf("Makefile has no %s target", target)
	}
	return lines
}

// TestMakefileMatchesCI keeps the local make targets aligned with the commands
// the CI workflow runs, so `make test` reproduces CI.
func TestMakefileMatchesCI(t *testing.T) {
	mk, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	ciRuns := func(job string) string {
		var b strings.Builder
		for _, s := range workflow.Jobs[job].Steps {
			b.WriteString(s.Run)
			b.WriteString("\n")
		}
		return b.String()
	}
	makefile := string(mk)

	for _, tt := range []struct {
		target, job, cmd string
	}{
		{"test", "build-vet-test", "go test -count=1 ./..."},
		{"test-race", "race", "go test -race -count=1 ./..."},
	} {
		recipe := makeRecipe(t, makefile, tt.target)
		if len(recipe) != 1 || recipe[0] != tt.cmd {
			t.Errorf("make %s runs %q, want %q", tt.target, recipe, tt.cmd)
		}
		if !strings.Contains(ciRuns(tt.job), tt.cmd) {
			t.Errorf("CI job %s no longer runs %q; update make %s to match", tt.job, tt.cmd, tt.target)
		}
	}

	fmtRecipe := strings.Join(makeRecipe(t, makefile, "fmt"), "\n")
	if !strings.Contains(fmtRecipe, "gofmt -l .") || !strings.Contains(fmtRecipe, "exit 1") {
		t.Errorf("make fmt must fail when gofmt -l . lists files, got:\n%s", fmtRecipe)
	}
	if !strings.Contains(ciRuns("build-vet-test"), "gofmt -l .") {
		t.Error("CI build-vet-test no longer runs gofmt -l .")
	}

	lintRecipe := strings.Join(makeRecipe(t, makefile, "lint"), "\n")
	for _, want := range []string{"go vet ./...", "staticcheck ./..."} {
		if !strings.Contains(lintRecipe, want) {
			t.Errorf("make lint does not run %q", want)
		}
	}
}
