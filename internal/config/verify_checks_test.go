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
