package config

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyOnStopChecksLoad(t *testing.T) {
	v := Default().VerifyOnStop
	if len(v.Checks) != 0 {
		t.Errorf("checks on by default: %v", v.Checks)
	}
	cfg, _, err := loadYAML(t, "verify_on_stop:\n  enabled: true\n  checks: [\"gofmt -l .\", \"go vet ./...\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.VerifyOnStop.Checks, "|"); got != "gofmt -l .|go vet ./..." {
		t.Errorf("checks = %q", got)
	}
	_, _, err = loadYAML(t, "verify_on_stop:\n  checks: [\"\"]\n")
	var ie *InvalidError
	if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), "verify_on_stop.checks") {
		t.Errorf("empty check: err = %v", err)
	}
}

func TestVerifyOnStopReviewNeedsChecks(t *testing.T) {
	if Default().VerifyOnStop.Review {
		t.Error("review on by default")
	}
	cfg, _, err := loadYAML(t, "verify_on_stop:\n  enabled: true\n  checks: [\"go vet ./...\"]\n  review: true\n")
	if err != nil || !cfg.VerifyOnStop.Review {
		t.Fatalf("review: %v, %v", cfg.VerifyOnStop.Review, err)
	}
	_, _, err = loadYAML(t, "verify_on_stop:\n  review: true\n")
	if err == nil || !strings.Contains(err.Error(), "verify_on_stop.review") {
		t.Errorf("review without checks: err = %v", err)
	}
}
