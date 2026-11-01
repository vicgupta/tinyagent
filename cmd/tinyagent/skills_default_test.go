package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/attempts"
)

// clearCIEnv makes the tests independent of the CI they run in.
func clearCIEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv(attempts.EnvAttempt, "")
}

// interactiveRun makes one-shot runs look like they are on a terminal.
func interactiveRun(t *testing.T, on bool) {
	t.Helper()
	prev := runIsInteractive
	runIsInteractive = func() bool { return on }
	t.Cleanup(func() { runIsInteractive = prev })
}

func TestResolveSkills(t *testing.T) { // #359
	tru, fal := true, false
	interactive := skillsMode{Interactive: true}
	nonTTY := skillsMode{}
	hardened := func(r string) skillsMode { return skillsMode{Interactive: true, Hardened: r} }
	for _, c := range []struct {
		name   string
		in     skillsInputs
		m      skillsMode
		on     bool
		source string
	}{
		// Mode defaults: every row of the table.
		{"interactive default", skillsInputs{}, interactive, true, skillsSourceInteractive},
		{"non-tty default", skillsInputs{}, nonTTY, false, skillsSourceHardened},
		{"CI default", skillsInputs{}, hardened("CI is set"), false, skillsSourceHardened},
		{"action default", skillsInputs{}, hardened("GITHUB_ACTIONS is set"), false, skillsSourceHardened},
		{"eval default", skillsInputs{}, hardened("--eval"), false, skillsSourceHardened},
		{"attempts default", skillsInputs{}, hardened("--attempts"), false, skillsSourceHardened},
		{"lockdown default", skillsInputs{}, hardened("lockdown"), false, skillsSourceHardened},
		{"read-only default", skillsInputs{}, hardened("--read-only"), false, skillsSourceHardened},
		// The flag beats everything.
		{"--no-skills interactive", skillsInputs{FlagSet: true, FlagName: "--no-skills"}, interactive, false, skillsSourceFlag},
		{"--no-skills beats config", skillsInputs{FlagSet: true, Config: &tru}, interactive, false, skillsSourceFlag},
		{"--skills beats config false", skillsInputs{FlagSet: true, FlagOn: true, Config: &fal}, interactive, true, skillsSourceFlag},
		{"--skills in eval", skillsInputs{FlagSet: true, FlagOn: true}, hardened("--eval"), true, skillsSourceFlag},
		{"--skills in CI", skillsInputs{FlagSet: true, FlagOn: true}, hardened("CI is set"), true, skillsSourceFlag},
		{"--skills in read-only", skillsInputs{FlagSet: true, FlagOn: true}, hardened("--read-only"), true, skillsSourceFlag},
		{"--skills non-tty with cwd config", skillsInputs{FlagSet: true, FlagOn: true, ConfigFromCwd: true}, nonTTY, true, skillsSourceFlag},
		// Config beats the mode default.
		{"config false interactive", skillsInputs{Config: &fal}, interactive, false, skillsSourceConfig},
		{"config false cwd interactive", skillsInputs{Config: &fal, ConfigFromCwd: true}, interactive, false, skillsSourceConfig},
		{"config true non-tty (-c)", skillsInputs{Config: &tru}, nonTTY, true, skillsSourceConfig},
		{"config true CI (-c)", skillsInputs{Config: &tru}, hardened("CI is set"), true, skillsSourceConfig},
		{"config true interactive cwd", skillsInputs{Config: &tru, ConfigFromCwd: true}, interactive, true, skillsSourceConfig},
		// A cwd config can't turn skills on in hardened modes untrusted.
		{"cwd config true CI", skillsInputs{Config: &tru, ConfigFromCwd: true}, hardened("CI is set"), false, skillsSourceHardened},
		{"cwd config true non-tty", skillsInputs{Config: &tru, ConfigFromCwd: true}, nonTTY, false, skillsSourceHardened},
		{"cwd config true read-only", skillsInputs{Config: &tru, ConfigFromCwd: true}, hardened("--read-only"), false, skillsSourceHardened},
		{"cwd config true eval trusted", skillsInputs{Config: &tru, ConfigFromCwd: true, TrustConfig: true}, hardened("--eval"), true, skillsSourceConfig},
	} {
		d := resolveSkills(c.in, c.m)
		if d.Enabled != c.on || d.Source != c.source {
			t.Errorf("%s: got %+v, want on=%v source %q", c.name, d, c.on, c.source)
		}
	}
	d := resolveSkills(skillsInputs{Config: &tru, ConfigFromCwd: true}, hardened("CI is set"))
	if !strings.Contains(d.String(), "--trust-config") || !strings.HasPrefix(d.String(), "off (hardened default: CI is set") {
		t.Errorf("ignored cwd config: %q", d)
	}
}

func TestHardenedReason(t *testing.T) {
	clearCIEnv(t)
	if r := hardenedReason(false, false, false, false); r != "" {
		t.Errorf("plain run hardened: %q", r)
	}
	for _, c := range []struct {
		eval, att, lock, ro bool
		want                string
	}{{true, false, false, false, "--eval"}, {false, true, false, false, "--attempts"}, {false, false, true, false, "lockdown"}, {false, false, false, true, "--read-only"}} {
		if r := hardenedReason(c.eval, c.att, c.lock, c.ro); r != c.want {
			t.Errorf("%+v: %q", c, r)
		}
	}
	t.Setenv("CI", "false")
	if r := hardenedReason(false, false, false, false); r != "" {
		t.Errorf("CI=false: %q", r)
	}
	t.Setenv("CI", "true")
	if r := hardenedReason(false, false, false, false); r != "CI is set" {
		t.Errorf("CI=true: %q", r)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	if r := hardenedReason(false, false, false, false); r != "GITHUB_ACTIONS is set" {
		t.Errorf("GITHUB_ACTIONS: %q", r)
	}
	clearCIEnv(t)
	t.Setenv(attempts.EnvAttempt, "1")
	if r := hardenedReason(false, false, false, false); r != "--attempts" {
		t.Errorf("attempt child: %q", r)
	}
}

func TestSkillsInteractiveDefault(t *testing.T) { // #359 acceptance
	var offered atomic.Bool
	skillsServer(t, &offered)
	dir := skillsRepo(t)
	cwdCfg := func(t *testing.T, body string) string {
		d := skillsRepo(t)
		os.WriteFile(filepath.Join(d, "tinyagent.yaml"), []byte(body), 0o644)
		return d
	}
	for _, c := range []struct {
		name        string
		interactive bool
		env         map[string]string
		dir         string
		args        []string
		want        bool
	}{
		{"interactive, no flag", true, nil, dir, nil, true},
		{"interactive --no-skills", true, nil, dir, []string{"--no-skills"}, false},
		{"interactive skills: false", true, nil, cwdCfg(t, "skills: false\n"), nil, false},
		{"non-tty", false, nil, dir, nil, false},
		{"non-tty --skills", false, nil, dir, []string{"--skills"}, true},
		{"CI", true, map[string]string{"CI": "true"}, dir, nil, false},
		{"CI --skills", true, map[string]string{"CI": "true"}, dir, []string{"--skills"}, true},
		{"GitHub Action", true, map[string]string{"GITHUB_ACTIONS": "true"}, dir, nil, false},
		{"read-only", true, nil, dir, []string{"--read-only"}, false},
		{"read-only --skills", true, nil, dir, []string{"--read-only", "--skills"}, true},
		{"lockdown", true, nil, dir, []string{"--lockdown"}, false},
		{"lockdown --skills", true, nil, dir, []string{"--lockdown", "--skills"}, true},
		{"CI cwd skills: true", true, map[string]string{"CI": "true"}, cwdCfg(t, "skills: true\n"), nil, false},
		{"CI cwd skills: true trusted", true, map[string]string{"CI": "true"}, cwdCfg(t, "skills: true\n"), []string{"--trust-config"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearCIEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			interactiveRun(t, c.interactive)
			offered.Store(false)
			code, _, stderr := runSkills(t, c.dir, c.args...)
			if code != 0 || offered.Load() != c.want {
				t.Errorf("exit %d, offered %v, want %v, stderr %q", code, offered.Load(), c.want, stderr)
			}
		})
	}
}

func TestSkillsResumeOldCheckpointStaysOff(t *testing.T) { // #359
	var offered atomic.Bool
	skillsServer(t, &offered)
	clearCIEnv(t)
	dir := skillsRepo(t)
	out := filepath.Join(t.TempDir(), "run.json")
	interactiveRun(t, false)
	if code, _, stderr := runSkills(t, dir, "-o", out); code != 0 || offered.Load() {
		t.Fatalf("first run: exit %d, offered %v, stderr %q", code, offered.Load(), stderr)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "skills_enabled") {
		t.Fatal("checkpoint records skills_enabled; want it absent like an old checkpoint")
	}
	// Resumed on a terminal with ./skills present: still off.
	interactiveRun(t, true)
	offered.Store(false)
	code, _, stderr := runCLI(t, "--resume", out, "-t", "more", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-o", out+".2")
	if code != 0 || offered.Load() {
		t.Errorf("resume: exit %d, offered %v, stderr %q", code, offered.Load(), stderr)
	}
}

func TestCheckReportsSkillsSource(t *testing.T) { // #359
	clearCIEnv(t)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := skillsRepo(t)
	cfgOff := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cfgOff, []byte("skills: false\n"), 0o644)
	cwdOn := skillsRepo(t)
	os.WriteFile(filepath.Join(cwdOn, "tinyagent.yaml"), []byte("skills: true\n"), 0o644)
	for _, c := range []struct {
		name        string
		interactive bool
		env         string
		dir         string
		args        []string
		line        string
		on          bool
		source      string
	}{
		{"interactive", true, "", dir, nil, "skills:    on (interactive default)", true, skillsSourceInteractive},
		{"flag", true, "", dir, []string{"--no-skills"}, "skills:    off (flag: --no-skills)", false, skillsSourceFlag},
		{"config", true, "", dir, []string{"-c", cfgOff}, "skills:    off (config)", false, skillsSourceConfig},
		{"non-tty", false, "", dir, nil, "skills:    off (hardened default: not an interactive terminal)", false, skillsSourceHardened},
		{"CI", true, "true", dir, nil, "skills:    off (hardened default: CI is set)", false, skillsSourceHardened},
		{"read-only", true, "", dir, []string{"--read-only"}, "skills:    off (hardened default: --read-only)", false, skillsSourceHardened},
		{"cwd config ignored", true, "true", cwdOn, nil, "skills:    off (hardened default: CI is set; skills: true in ./tinyagent.yaml ignored without --trust-config)", false, skillsSourceHardened},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CI", c.env)
			interactiveRun(t, c.interactive)
			base := append([]string{"--check", "-m", "openai/gpt-4o", "--cwd", c.dir}, c.args...)
			code, stdout, stderr := runCLI(t, base...)
			if code != 0 || !strings.Contains(stdout, c.line) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			code, stdout, _ = runCLI(t, append(base, "--json")...)
			var sum struct {
				Skills skillsDecision `json:"skills"`
			}
			if err := json.Unmarshal([]byte(stdout), &sum); code != 0 || err != nil || sum.Skills.Enabled != c.on || sum.Skills.Source != c.source {
				t.Errorf("--json: exit %d, %v, %+v from %q", code, err, sum.Skills, stdout)
			}
		})
	}
}
