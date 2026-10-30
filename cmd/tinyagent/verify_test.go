package main

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestApplyVerifyFlags(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"--verify-on-stop", "--verify-command", "go test ./...", "--verify-command", "make lint", "--max-verify-reprompts", "4"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.VerifyOnStop.Commands = []string{"from config"}
	if err := applyVerifyFlags(fs, &cfg, o.verify); err != nil {
		t.Fatal(err)
	}
	v := cfg.VerifyOnStop
	if !v.Enabled || strings.Join(v.Commands, "|") != "go test ./...|make lint" || v.MaxReprompts != 4 {
		t.Errorf("verify_on_stop = %+v", v)
	}

	var o2 options
	fs = newFlagSet(&o2)
	if err := fs.Parse([]string{"--max-verify-reprompts", "0"}); err != nil {
		t.Fatal(err)
	}
	cfg = config.Default()
	if err := applyVerifyFlags(fs, &cfg, o2.verify); err == nil {
		t.Error("--max-verify-reprompts 0 accepted")
	}

	var o3 options
	fs = newFlagSet(&o3)
	cfg = config.Default()
	cfg.VerifyOnStop.Enabled = true
	if err := applyVerifyFlags(fs, &cfg, o3.verify); err != nil || !cfg.VerifyOnStop.Enabled {
		t.Errorf("unset flags changed the config: %+v, %v", cfg.VerifyOnStop, err)
	}
}

func TestUnverifiedOutput(t *testing.T) {
	tr := &trajectory.Trajectory{ExitStatus: "Completed", Steps: 3, Unverified: true, VerifyReprompts: 2}
	if s := summaryLine(tr, 0); !strings.Contains(s, ", unverified changes") {
		t.Errorf("summary = %q", s)
	}
	f := resultFields(tr, 0)
	if f["unverified"] != true || f["verify_reprompts"] != 2 {
		t.Errorf("fields = %v", f)
	}
	tr = &trajectory.Trajectory{ExitStatus: "Completed", Steps: 3}
	if s := summaryLine(tr, 0); strings.Contains(s, "unverified") {
		t.Errorf("summary = %q", s)
	}
	f = resultFields(tr, 0)
	if _, ok := f["unverified"]; ok {
		t.Errorf("fields = %v", f)
	}
	if _, ok := f["verify_reprompts"]; ok {
		t.Errorf("fields = %v", f)
	}
}
