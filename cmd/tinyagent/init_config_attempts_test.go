package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

// The starter config's attempts block, once uncommented, is a valid
// attempts section with one check.
func TestStarterConfigAttemptsBlock(t *testing.T) {
	if !strings.Contains(starterConfig, "\n# attempts:") {
		t.Fatal("starter config has no attempts block")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(starterConfig, "\n# ", "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Attempts
	if len(a.Checks) != 1 || a.Checks[0].Command != "go test ./..." || a.Checks[0].TimeoutSeconds != 600 ||
		!a.StopsOnPass() || a.KeepMode() != config.AttemptsKeepAll {
		t.Fatalf("attempts = %+v", a)
	}
}
