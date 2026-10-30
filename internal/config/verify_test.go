package config

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyOnStopDefaults(t *testing.T) {
	v := Default().VerifyOnStop
	if v.Enabled || len(v.Commands) != 0 || len(v.Patterns) != 0 || v.MaxReprompts != DefaultVerifyMaxReprompts {
		t.Errorf("defaults = %+v", v)
	}
	if err := v.Validate(); err != nil {
		t.Error(err)
	}
}

func TestVerifyOnStopLoad(t *testing.T) {
	cfg, _, err := loadYAML(t, "verify_on_stop:\n  enabled: true\n  commands: [\"go test ./...\"]\n  patterns: [\"^make\\\\b\"]\n  max_reprompts: 3\n")
	if err != nil {
		t.Fatal(err)
	}
	v := cfg.VerifyOnStop
	if !v.Enabled || len(v.Commands) != 1 || v.Commands[0] != "go test ./..." || len(v.Patterns) != 1 || v.MaxReprompts != 3 {
		t.Errorf("verify_on_stop = %+v", v)
	}
	cfg, _, err = loadYAML(t, "verify_on_stop:\n  enabled: true\n")
	if err != nil || cfg.VerifyOnStop.MaxReprompts != DefaultVerifyMaxReprompts {
		t.Errorf("partial block: %+v, %v", cfg.VerifyOnStop, err)
	}
}

func TestVerifyOnStopInvalid(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"verify_on_stop:\n  max_reprompts: 0\n", "verify_on_stop.max_reprompts"},
		{"verify_on_stop:\n  max_reprompts: 11\n", "verify_on_stop.max_reprompts"},
		{"verify_on_stop:\n  commands: [\" \"]\n", "verify_on_stop.commands"},
		{"verify_on_stop:\n  patterns: [\"(\"]\n", "verify_on_stop.patterns"},
	} {
		_, _, err := loadYAML(t, c.body)
		var ie *InvalidError
		if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want InvalidError mentioning %q", c.body, err, c.want)
		}
	}
}

func TestDefaultVerifyPatternsCompile(t *testing.T) {
	if err := (VerifyOnStop{Patterns: DefaultVerifyPatterns, MaxReprompts: 1}).Validate(); err != nil {
		t.Error(err)
	}
}
