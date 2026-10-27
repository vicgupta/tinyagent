package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestCheckWorkers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	on := config.Default()
	setWorkers(&on, true)
	if err := checkWorkers(config.Default(), true, t.TempDir()); err != nil {
		t.Errorf("workers off: %v", err)
	}
	ro := on
	ro.ReadOnly = true
	plain := t.TempDir()
	empty := t.TempDir()
	if out, err := exec.Command("git", "-C", empty, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "a"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, c := range []struct {
		name   string
		cfg    config.AgentConfig
		review bool
		dir    string
		want   string
	}{
		{"review", on, true, repo, "--review"},
		{"read-only", ro, false, repo, "--read-only"},
		{"not git", on, false, plain, "needs a Git repository"},
		{"no commits", on, false, empty, "needs a commit"},
		{"ok", on, false, repo, ""},
	} {
		err := checkWorkers(c.cfg, c.review, c.dir)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestSetWorkersDoesNotShareConfig(t *testing.T) {
	a := config.Default()
	setWorkers(&a, false)
	if a.Subagents.Workers != nil {
		t.Error("setWorkers(false) created a section")
	}
	setWorkers(&a, true)
	b := a
	setWorkers(&b, false)
	if !a.Subagents.Workers.Enabled || b.Subagents.Workers.Enabled {
		t.Errorf("a=%+v b=%+v", a.Subagents.Workers, b.Subagents.Workers)
	}
}

func TestNewWorkers(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	if w, err := newWorkers(config.Default(), "openai/gpt-5-mini", ""); w != nil || err != nil {
		t.Errorf("off: %v, %v", w, err)
	}
	cfg := config.Default()
	setWorkers(&cfg, true)
	cfg.Subagents.Workers.MaxSteps = 7
	w, err := newWorkers(cfg, "openai/gpt-5-mini", "")
	if err != nil || w == nil || w.ModelName != "openai/gpt-5-mini" || w.MaxSteps != 7 {
		t.Errorf("on: %+v, %v", w, err)
	}
	cfg.Subagents.Workers.Model = "nope/model"
	if _, err := newWorkers(cfg, "openai/gpt-5-mini", ""); err == nil || !strings.Contains(err.Error(), "subagents.workers.model") {
		t.Errorf("bad model: %v", err)
	}
}
