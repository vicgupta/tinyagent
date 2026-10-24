package tinyagent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Exercise the workflow's actual shell commands so merely listing unformatted
// files (gofmt's default behavior) cannot accidentally make the check pass.
func TestCIQualityChecks(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name       string
		command    string
		source     string
		wantFail   bool
		wantOutput string
	}{
		{"formatted", "gofmt", "package fixture\n\nvar Answer = 42\n", false, ""},
		{"unformatted", "gofmt", "package fixture\nvar Answer=42\n", true, "fixture_test.go"},
		{"invalid syntax", "gofmt", "package fixture\n\nfunc (\n", true, "expected"},
		{"passing test", "go test", "package fixture\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {}\n", false, "ok"},
		{"failing test", "go test", "package fixture\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {\n\tt.Fatal(\"intentional failure\")\n}\n", true, "intentional failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var script string
			for _, step := range workflow.Jobs["build-vet-test"].Steps {
				if strings.Contains(step.Run, tt.command) {
					script = step.Run
					break
				}
			}
			if script == "" {
				t.Fatalf("CI has no %s check", tt.command)
			}
			dir := t.TempDir()
			for name, content := range map[string]string{
				"go.mod":          "module fixture\n\ngo 1.27.1\n",
				"fixture_test.go": tt.source,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantFail || !strings.Contains(string(output), tt.wantOutput) {
				t.Fatalf("CI check: err=%v, output=%s; want failure=%v, output containing %q", err, output, tt.wantFail, tt.wantOutput)
			}
		})
	}
}
