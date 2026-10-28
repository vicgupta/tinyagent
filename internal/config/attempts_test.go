package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAttemptsConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAttemptsConfig(t *testing.T) {
	cfg, err := Load(writeAttemptsConfig(t, "attempts:\n  checks:\n    - command: go test ./...\n      timeout_seconds: 30\n    - command: make lint\n  stop_on_pass: false\n  keep: none\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Attempts
	if len(a.Checks) != 2 || a.Checks[0].Command != "go test ./..." || a.Checks[0].TimeoutSeconds != 30 || a.Checks[1].TimeoutSeconds != 0 {
		t.Fatalf("checks = %+v", a.Checks)
	}
	if a.StopsOnPass() || a.KeepMode() != AttemptsKeepNone {
		t.Fatalf("attempts = %+v", a)
	}

	d := Default().Attempts
	if !d.StopsOnPass() || d.KeepMode() != AttemptsKeepAll || len(d.Checks) != 0 {
		t.Fatalf("defaults = %+v", d)
	}
}

func TestAttemptsConfigInvalid(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"attempts:\n  keep: some\n", "attempts.keep"},
		{"attempts:\n  checks:\n    - command: \"  \"\n", "command must not be empty"},
		{"attempts:\n  checks:\n    - command: x\n      timeout_seconds: 3601\n", "timeout_seconds"},
		{"attempts:\n  checks:\n    - command: x\n      timeout_seconds: -1\n", "timeout_seconds"},
		{"attempts:\n  stop_on_pas: false\n", "stop_on_pas"},
		{"attempts:\n  checks:\n    - cmd: x\n", "cmd"},
		{"attempts: [x]\n", "attempts must be a mapping"},
	} {
		_, err := Load(writeAttemptsConfig(t, c.body))
		var inv *InvalidError
		if err == nil || !errors.As(err, &inv) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want InvalidError containing %q", c.body, err, c.want)
		}
	}
}
