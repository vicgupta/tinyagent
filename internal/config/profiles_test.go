package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeProfileConfig(t *testing.T, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const profileBase = `model: openai/a
cost_limit: 5
step_limit: 40
fallback_models: [openai/x, openai/y]
snapshots: true
web_fetch:
  allowed_domains: [pkg.go.dev]
profiles:
  review:
    model: openai/b
    cost_limit: 0.5
  fetch:
    web_fetch: {enabled: true}
  chain:
    fallback_models: [openai/z]
  snaps:
    snapshots: {enabled: true, max_file_bytes: 1024}
  policy:
    command_env: {inherit: core}
    file_access: {deny_read: [".env"]}
`

func TestProfileDefaultUnchanged(t *testing.T) { // AC1
	with, err := Load(writeProfileConfig(t, profileBase))
	if err != nil {
		t.Fatal(err)
	}
	base := profileBase[:strings.Index(profileBase, "profiles:")]
	without, err := Load(writeProfileConfig(t, base))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(with, without) {
		t.Errorf("profiles: changed the base config:\n%+v\n%+v", with, without)
	}
}

func TestProfileOverlay(t *testing.T) { // AC2, AC4-AC7
	p := writeProfileConfig(t, profileBase)
	load := func(name string) AgentConfig {
		t.Helper()
		cfg, err := LoadProfile(p, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return cfg
	}
	if c := load("review"); c.Model != "openai/b" || c.CostLimit != 0.5 || c.StepLimit != 40 {
		t.Errorf("review: model %q cost %v steps %d", c.Model, c.CostLimit, c.StepLimit)
	}
	if c := load("fetch"); !c.WebFetch.Enabled || !reflect.DeepEqual(c.WebFetch.AllowedDomains, []string{"pkg.go.dev"}) {
		t.Errorf("fetch: web_fetch %+v", c.WebFetch)
	}
	if c := load("chain"); !reflect.DeepEqual(c.FallbackModels, []string{"openai/z"}) {
		t.Errorf("chain: fallback_models %v", c.FallbackModels)
	}
	if c := load("snaps"); !c.Snapshots.Enabled || c.Snapshots.MaxFileBytes != 1024 {
		t.Errorf("snaps: snapshots %+v", c.Snapshots)
	}
	if c := load("policy"); c.CommandEnv.Inherit != "core" || !reflect.DeepEqual(c.FileAccess.DenyRead, []string{".env"}) {
		t.Errorf("policy: command_env %+v file_access %+v", c.CommandEnv, c.FileAccess)
	}
	// The base is untouched by a selected profile's merge.
	if c := load(""); c.Model != "openai/a" || c.WebFetch.Enabled || len(c.FallbackModels) != 2 {
		t.Errorf("base: %+v", c)
	}
}

func TestProfileErrors(t *testing.T) { // AC8-AC12
	for _, c := range []struct {
		name, data, profile string
		want                []string
	}{
		{"unknown", profileBase, "nope", []string{`unknown profile "nope"`, "chain, fetch, policy, review, snaps"}},
		{"no section", "model: openai/a\n", "x", []string{"defines no profiles"}},
		{"typo unselected", "profiles:\n  ci:\n    step_limt: 5\n  ok: {}\n", "", []string{"profile ci", "step_limt"}},
		{"typo selected", "profiles:\n  ci:\n    step_limt: 5\n", "ci", []string{"profile ci", "step_limt"}},
		{"bad value", "profiles:\n  review: {max_format_errors: 0}\n", "review", []string{"max_format_errors", "(profile review)"}},
		{"bad name", "profiles:\n  \"a b\": {}\n", "", []string{`profile name "a b" is invalid`}},
		{"not mapping", "profiles:\n  ci: true\n", "", []string{"profile ci must be a mapping"}},
		{"section not mapping", "profiles: [ci]\n", "", []string{"profiles must be a mapping"}},
		{"nested", "profiles:\n  ci:\n    profiles: {x: {}}\n", "", []string{"profiles can't be nested"}},
		{"null", "profiles:\n  ci:\n    web_fetch: {allowed_domains: null}\n", "", []string{"web_fetch.allowed_domains is null"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := writeProfileConfig(t, c.data)
			_, err := LoadProfile(p, c.profile)
			var invalid *InvalidError
			if err == nil || !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want an InvalidError", err)
			}
			for _, w := range append(c.want, p) {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestProfileWithoutConfigFile(t *testing.T) { // AC9
	_, err := LoadProfile("", "x")
	var invalid *InvalidError
	if err == nil || !errors.As(err, &invalid) || !strings.Contains(err.Error(), "no config file defines profiles") {
		t.Errorf("err = %v", err)
	}
	if _, err := LoadProfile("", ""); err != nil {
		t.Errorf("no profile, no file: %v", err)
	}
}

func TestProfileNames(t *testing.T) {
	p := writeProfileConfig(t, "profiles:\n  zebra: {}\n  apple: {}\n  banana: {}\n")
	names, err := ProfileNames(p)
	if err != nil || !reflect.DeepEqual(names, []string{"apple", "banana", "zebra"}) {
		t.Errorf("names = %v, err %v", names, err)
	}
	if names, err := ProfileNames(""); err != nil || len(names) != 0 {
		t.Errorf("empty path: %v, %v", names, err)
	}
	p = writeProfileConfig(t, "model: openai/a\n")
	if names, err := ProfileNames(p); err != nil || len(names) != 0 {
		t.Errorf("no section: %v, %v", names, err)
	}
	if _, err := ProfileNames(filepath.Join(t.TempDir(), "nope.yaml")); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("missing file: %v", err)
	}
	p = writeProfileConfig(t, "profiles:\n  \"a b\": {}\n")
	if _, err := ProfileNames(p); err == nil || !strings.Contains(err.Error(), "profile name") {
		t.Errorf("invalid name: %v", err)
	}
}

func TestProfileListReplaceAndKindChange(t *testing.T) {
	p := writeProfileConfig(t, "hooks:\n  stop:\n    - command: /bin/true\nprofiles:\n  quiet:\n    hooks: {stop: []}\n  big:\n    snapshots: {max_file_bytes: 7}\n")
	cfg, err := LoadProfile(p, "quiet")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.Stop) != 0 {
		t.Errorf("hooks.stop = %v, want empty", cfg.Hooks.Stop)
	}
	if cfg, err = LoadProfile(p, "big"); err != nil || cfg.Snapshots.MaxFileBytes != 7 {
		t.Errorf("big: %v %+v", err, cfg.Snapshots)
	}
}
