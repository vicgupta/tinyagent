package config

import (
	"errors"
	"strings"
	"testing"
)

// Unknown or mistyped keys under untrusted_content are InvalidErrors (exit
// status 2), so a typo can't silently leave lockdown off or looser than
// written.
func TestUntrustedContentUnknownKeysInvalid(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"untrusted_content:\n  enable: true\n", "field enable not found"},
		{"untrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domain: [x.com]\n", "field allowed_domain not found"},
		{"untrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domains: [x.com]\n", "field allowed_domains not found"},
		{"untrusted_content:\n  enabled: maybe\n", "untrusted_content"},
		{"untrusted_content: true\n", "untrusted_content must be a mapping"},
	} {
		_, _, err := loadYAML(t, c.body)
		var ie *InvalidError
		if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), "untrusted_content") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want InvalidError mentioning untrusted_content and %q", c.body, err, c.want)
		}
	}
	// A valid or empty section still loads.
	for _, body := range []string{
		"untrusted_content:\n",
		"untrusted_content: {}\n",
		"untrusted_content:\n  enabled: true\n  sources: [mcp]\n  lockdown:\n    web_fetch_allowed_domains: [pkg.go.dev]\n",
	} {
		if _, _, err := loadYAML(t, body); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// An unknown untrusted_content key in a profile is rejected too.
func TestUntrustedContentUnknownKeyInProfile(t *testing.T) {
	_, path, _ := loadYAML(t, "profiles:\n  ci:\n    untrusted_content:\n      enable: true\n")
	if _, err := LoadProfile(path, "ci"); err == nil || !strings.Contains(err.Error(), "enable") {
		t.Errorf("err = %v", err)
	}
}
