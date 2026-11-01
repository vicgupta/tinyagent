package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

const profileCLIConfig = `model: openai/gpt-4o
cost_limit: 5
step_limit: 40
profiles:
  review:
    model: openai/gpt-4o-mini
    cost_limit: 0.5
  ci: {}
`

func profileSetup(t *testing.T) (dir string, bodies *[]map[string]any) {
	t.Helper()
	bodies = &[]map[string]any{}
	srv := fakeOpenAIBodies(t, bodies)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // os.UserConfigDir ignores XDG_CONFIG_HOME on macOS
	return t.TempDir(), bodies
}

func TestProfileRun(t *testing.T) { // AC2, AC13
	dir, bodies := profileSetup(t)
	cfg := writeConfig(t, profileCLIConfig)
	traj := filepath.Join(dir, "run.json")
	code, stdout, stderr := runCLI(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg, "--profile", "review", "-o", traj, "--json", "-v")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(*bodies) == 0 || (*bodies)[0]["model"] != "gpt-4o-mini" {
		t.Errorf("request model = %v, want gpt-4o-mini", (*bodies)[0]["model"])
	}
	if started := runStarted(t, stdout); started["profile"] != "review" {
		t.Errorf("run.started profile = %v", started["profile"])
	}
	b, _ := os.ReadFile(traj)
	var tr trajectory.Trajectory
	json.Unmarshal(b, &tr)
	if tr.Profile != "review" || !strings.Contains(string(b), `"profile": "review"`) {
		t.Errorf("trajectory profile = %q", tr.Profile)
	}
	if !strings.Contains(stderr, "config profile: review (from "+cfg+")") {
		t.Errorf("-v lacks the profile line:\n%s", stderr)
	}

	// Without --profile, neither the trajectory nor run.started has one.
	*bodies = nil
	code, stdout, stderr = runCLI(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg, "-o", traj, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if (*bodies)[0]["model"] != "gpt-4o" {
		t.Errorf("base request model = %v", (*bodies)[0]["model"])
	}
	if _, ok := runStarted(t, stdout)["profile"]; ok {
		t.Error("run.started has a profile without --profile")
	}
	if b, _ := os.ReadFile(traj); strings.Contains(string(b), `"profile"`) {
		t.Error("trajectory has a profile without --profile")
	}
}

func TestProfileCheck(t *testing.T) { // AC2, AC3
	dir, _ := profileSetup(t)
	cfg := writeConfig(t, profileCLIConfig)
	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir, "-c", cfg, "--profile", "review")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, s := range []string{"profile:   review", "openai/gpt-4o-mini", "steps 40", "cost $0.50"} {
		if !strings.Contains(stdout, s) {
			t.Errorf("--check output lacks %q:\n%s", s, stdout)
		}
	}
	code, stdout, stderr = runCLI(t, "--check", "--cwd", dir, "-c", cfg, "--profile", "review", "--cost-limit", "2")
	if code != 0 || !strings.Contains(stdout, "cost $2.00 (from --cost-limit)") {
		t.Errorf("flag should win: exit %d\n%s%s", code, stdout, stderr)
	}
}

func TestProfileErrorsExit2(t *testing.T) { // AC8-AC12
	dir, bodies := profileSetup(t)
	cfg := writeConfig(t, profileCLIConfig)
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"unknown", []string{"-c", cfg, "--profile", "nope"}, []string{cfg, `unknown profile "nope"`, "ci, review"}},
		{"no config", []string{"--profile", "x"}, []string{"no config file defines profiles"}},
		{"typo unselected", []string{"-c", writeConfig(t, "profiles:\n  ci:\n    step_limt: 5\n")}, []string{"profile ci", "step_limt"}},
		{"bad value", []string{"-c", writeConfig(t, "profiles:\n  review: {max_format_errors: 0}\n"), "--profile", "review"}, []string{"max_format_errors", "(profile review)"}},
		{"bad name", []string{"-c", writeConfig(t, "profiles:\n  a.b: {}\n")}, []string{`profile name "a.b" is invalid`}},
		{"rewind", []string{"--rewind", filepath.Join(dir, "x.json"), "--profile", "ci"}, []string{"--profile doesn't apply to --rewind"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, append([]string{"-t", "x", "--no-stream", "--cwd", dir}, c.args...)...)
			if code != 2 {
				t.Errorf("exit %d, want 2: %s", code, stderr)
			}
			for _, w := range c.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr lacks %q:\n%s", w, stderr)
				}
			}
		})
	}
	if len(*bodies) != 0 {
		t.Errorf("%d model requests, want 0", len(*bodies))
	}
}

func TestProfileExplicitConfigOnly(t *testing.T) { // AC14
	dir, _ := profileSetup(t)
	user := userConfigPath(t)
	os.MkdirAll(filepath.Dir(user), 0o755)
	os.WriteFile(user, []byte("profiles:\n  mine: {}\n"), 0o644)
	team := writeConfig(t, "profiles:\n  ci: {step_limit: 7}\n")
	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir, "-c", team, "--profile", "ci")
	if code != 0 || !strings.Contains(stdout, "steps 7") {
		t.Errorf("-c team.yaml --profile ci: exit %d\n%s%s", code, stdout, stderr)
	}
	code, _, stderr = runCLI(t, "--check", "--cwd", dir, "-c", team, "--profile", "mine")
	if code != 2 || !strings.Contains(stderr, team) || !strings.Contains(stderr, "defined profiles: ci)") {
		t.Errorf("user-config profile used with -c: exit %d: %s", code, stderr)
	}
	// The old user config is no longer read.
	code, _, stderr = runCLI(t, "--check", "--cwd", dir, "--profile", "mine")
	if code != 2 {
		t.Errorf("user config profile: exit %d, want 2\n%s", code, stderr)
	}
	os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("profiles:\n  mine: {}\n"), 0o644)
	code, stdout, stderr = runCLI(t, "--check", "--cwd", dir, "--profile", "mine")
	if code != 0 || !strings.Contains(stdout, "profile:   mine") {
		t.Errorf("project config profile: exit %d\n%s%s", code, stdout, stderr)
	}
}

func TestProfileResume(t *testing.T) {
	dir, bodies := profileSetup(t)
	cfg := writeConfig(t, profileCLIConfig)
	first := filepath.Join(dir, "a.json")
	if code, _, stderr := runCLI(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg, "--profile", "review", "-o", first); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	second := filepath.Join(dir, "b.json")
	code, _, stderr := runCLI(t, "--resume", first, "-t", "more", "--no-stream", "--cwd", dir, "-c", cfg, "--profile", "ci", "-o", second)
	if code != 0 {
		t.Fatalf("resume exit %d: %s", code, stderr)
	}
	b, _ := os.ReadFile(second)
	var tr trajectory.Trajectory
	json.Unmarshal(b, &tr)
	// The saved model still wins; the profile recorded is this run's.
	if tr.Profile != "ci" || tr.Model != "openai/gpt-4o-mini" || (*bodies)[len(*bodies)-1]["model"] != "gpt-4o-mini" {
		t.Errorf("resumed profile %q model %q", tr.Profile, tr.Model)
	}
	if code, _, stderr := runCLI(t, "--resume", first, "-t", "more", "--no-stream", "--cwd", dir, "-c", cfg, "--profile", "nope", "-o", second); code != 2 {
		t.Errorf("resume with unknown profile: exit %d: %s", code, stderr)
	}
}
