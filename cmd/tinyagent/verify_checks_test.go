package main

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestApplyVerifyCheckFlag(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"--verify-check", "gofmt -l .", "--verify-check", "go test ./..."}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.VerifyOnStop.Checks = []string{"from config"}
	if err := applyVerifyFlags(fs, &cfg, o.verify); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.VerifyOnStop.Checks, "|"); got != "gofmt -l .|go test ./..." {
		t.Errorf("checks = %q", got)
	}

	var o2 options
	fs = newFlagSet(&o2)
	cfg = config.Default()
	cfg.VerifyOnStop.Checks = []string{"from config"}
	if err := applyVerifyFlags(fs, &cfg, o2.verify); err != nil || len(cfg.VerifyOnStop.Checks) != 1 {
		t.Errorf("unset flag changed the checks: %v, %v", cfg.VerifyOnStop.Checks, err)
	}

	var o3 options
	fs = newFlagSet(&o3)
	if err := fs.Parse([]string{"--verify-check", " "}); err == nil {
		t.Error("blank --verify-check accepted")
	}
}

func TestApplyVerifyReviewFlag(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"--verify-check", "go vet ./...", "--verify-review"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := applyVerifyFlags(fs, &cfg, o.verify); err != nil || !cfg.VerifyOnStop.Review {
		t.Fatalf("review = %v, err = %v", cfg.VerifyOnStop.Review, err)
	}

	var o2 options
	fs = newFlagSet(&o2)
	if err := fs.Parse([]string{"--verify-review"}); err != nil {
		t.Fatal(err)
	}
	cfg = config.Default()
	if err := applyVerifyFlags(fs, &cfg, o2.verify); err == nil {
		t.Error("--verify-review without checks accepted")
	}
}
