package main

import (
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestReadTask(t *testing.T) {
	cases := []struct {
		task, stdin, want string
	}{
		{"fix the bug", "ignored", "fix the bug"},
		{"-", "line one\nline two\n", "line one\nline two"},
		{"-", "  padded  \n\n", "padded"},
	}
	for _, c := range cases {
		got, err := readTask(c.task, strings.NewReader(c.stdin))
		if err != nil || got != c.want {
			t.Errorf("readTask(%q, %q) = %q, %v; want %q", c.task, c.stdin, got, err, c.want)
		}
	}
}

func TestReadTaskEmptyStdin(t *testing.T) {
	if _, err := readTask("-", strings.NewReader(" \n")); err == nil {
		t.Error("want an error for an empty task on stdin")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestReadTaskStdinError(t *testing.T) {
	if _, err := readTask("-", errReader{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want the read error, got %v", err)
	}
}

func TestApplyFlagOverridesTimeLimit(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		cfgWall int
		want    int
	}{
		{"no flag keeps default", nil, 0, 0},
		{"no flag keeps config", nil, 60, 60},
		{"flag wins over config", []string{"--time-limit", "5"}, 60, 5},
		{"explicit zero clears config", []string{"--time-limit", "0"}, 60, 0},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		var step, timeLimit int
		var cost float64
		fs.IntVar(&step, "step-limit", 0, "")
		fs.Float64Var(&cost, "cost-limit", 0, "")
		fs.IntVar(&timeLimit, "time-limit", 0, "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatalf("%s: parse: %v", c.name, err)
		}
		cfg := config.AgentConfig{WallTimeSec: c.cfgWall, StepLimit: 7, CostLimit: 1.5}
		applyFlagOverrides(fs, &cfg, step, cost, timeLimit)
		if cfg.WallTimeSec != c.want {
			t.Errorf("%s: WallTimeSec = %d, want %d", c.name, cfg.WallTimeSec, c.want)
		}
		if cfg.StepLimit != 7 || cfg.CostLimit != 1.5 {
			t.Errorf("%s: unrelated limits changed: %+v", c.name, cfg)
		}
	}
}
