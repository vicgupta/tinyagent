package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPermissionJudgeParses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, `
permissions:
  default: ask
  judge:
    enabled: true
    min_confidence: 0.9
    timeout_seconds: 15
`)
	cfg, err := load(t, p)
	if err != nil {
		t.Fatal(err)
	}
	j := cfg.Permissions.Judge
	if !j.On() || j.ConfidenceThreshold() != 0.9 || j.TimeoutSeconds() != 15 {
		t.Fatalf("judge = %+v", j)
	}
}

func TestPermissionJudgeDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "permissions:\n  default: ask\n  judge:\n    enabled: true\n")
	cfg, err := load(t, p)
	if err != nil {
		t.Fatal(err)
	}
	j := cfg.Permissions.Judge
	if !j.On() || j.ConfidenceThreshold() != DefaultPermissionJudgeMinConfidence || j.TimeoutSeconds() != DefaultPermissionJudgeTimeoutSec {
		t.Fatalf("defaults = %+v thresh=%g timeout=%d", j, j.ConfidenceThreshold(), j.TimeoutSeconds())
	}
}

func TestPermissionJudgeInvalid(t *testing.T) {
	cases := []struct {
		yaml, want string
	}{
		{"permissions:\n  judge:\n    enabled: true\n    min_confidence: 1.5\n", "min_confidence"},
		{"permissions:\n  judge:\n    enabled: true\n    timeout_seconds: 0\n", "timeout_seconds"},
		{"permissions:\n  judge:\n    enabled: true\n    timeout_seconds: 99\n", "timeout_seconds"},
		{"permissions:\n  judge:\n    enabled: true\n    typo: 1\n", "typo"},
	}
	for _, c := range cases {
		p := filepath.Join(t.TempDir(), "c.yaml")
		write(t, p, c.yaml)
		_, err := load(t, p)
		var invalid *InvalidError
		if !errors.As(err, &invalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("yaml %q: err = %v, want *InvalidError naming %q", c.yaml, err, c.want)
		}
	}
}

func TestPermissionJudgeAloneConfigures(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "permissions:\n  judge:\n    enabled: true\n")
	cfg, err := load(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Permissions.Configured() || !cfg.Permissions.Judge.On() {
		t.Fatalf("configured = %+v", cfg.Permissions)
	}
}
