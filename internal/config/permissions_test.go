package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPermissionsConfigParses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, `
permissions:
  default: allow
  non_interactive: deny
  rules:
    - tool: "bash|start_process"
      command: "git push *"
      decision: ask
    - tool: "write_file|edit_file|apply_patch"
      path: "migrations/**"
      decision: deny
      message: "no migrations"
    - tool: "web_fetch"
      host: "*.internal.example.com"
      decision: ask
`)
	cfg, err := load(t, p)
	if err != nil {
		t.Fatal(err)
	}
	perm := cfg.Permissions
	if perm.Default != "allow" || perm.NonInteractive != "deny" || len(perm.Rules) != 3 {
		t.Fatalf("permissions = %+v", perm)
	}
	if perm.Rules[0].Tool != "bash|start_process" || perm.Rules[0].Decision != "ask" {
		t.Errorf("rules[0] = %+v", perm.Rules[0])
	}
	if perm.Rules[1].Path != "migrations/**" || perm.Rules[1].Message != "no migrations" {
		t.Errorf("rules[1] = %+v", perm.Rules[1])
	}
	if perm.Rules[2].Host != "*.internal.example.com" {
		t.Errorf("rules[2] = %+v", perm.Rules[2])
	}
}

func TestPermissionsInvalidDecision(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "permissions:\n  rules:\n    - tool: bash\n      command: \"curl *\"\n      decision: nope\n")
	_, err := load(t, p)
	var invalid *InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *InvalidError", err)
	}
	if !strings.Contains(err.Error(), "permissions.rules[0]") || !strings.Contains(err.Error(), "decision") {
		t.Errorf("err = %v", err)
	}
}

func TestPermissionsUnknownKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "permissions:\n  default: allow\n  typo: true\n")
	_, err := load(t, p)
	var invalid *InvalidError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("err = %v, want an *InvalidError naming typo", err)
	}
}

func TestPermissionsProfileReplacesRules(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, `
permissions:
  default: allow
  rules:
    - tool: bash
      command: "curl *"
      decision: deny
profiles:
  careful:
    permissions:
      rules:
        - tool: bash
          command: "git push *"
          decision: ask
`)
	cfg, err := LoadProfile(p, "careful")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Permissions.Default != "allow" {
		t.Errorf("default = %q, want the base allow kept", cfg.Permissions.Default)
	}
	if len(cfg.Permissions.Rules) != 1 || cfg.Permissions.Rules[0].Command != "git push *" {
		t.Errorf("rules = %+v, want the profile's rules to replace the base list", cfg.Permissions.Rules)
	}
}

func TestPermissionsPathOnBash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "permissions:\n  rules:\n    - tool: bash\n      path: \"x\"\n      decision: deny\n")
	_, err := load(t, p)
	var invalid *InvalidError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("err = %v, want a matcher/tool mismatch", err)
	}
}
