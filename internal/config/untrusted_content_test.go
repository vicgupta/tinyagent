package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestUntrustedContentOffByDefault(t *testing.T) {
	u := Default().UntrustedContent
	if u.Enabled || len(u.Lockdown.WebFetchAllowedDomains) != 0 {
		t.Errorf("defaults = %+v", u)
	}
}

func TestUntrustedContentLoad(t *testing.T) {
	cfg, _, err := loadYAML(t, "untrusted_content:\n  enabled: true\n  lockdown:\n    web_fetch_allowed_domains: [pkg.go.dev, \"*.golang.org\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	u := cfg.UntrustedContent
	if !u.Enabled || !slices.Equal(u.Lockdown.WebFetchAllowedDomains, []string{"pkg.go.dev", "*.golang.org"}) {
		t.Errorf("untrusted_content = %+v", u)
	}
}

func TestUntrustedContentInvalid(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"untrusted_content:\n  lockdown:\n    web_fetch_allowed_domains: [\"*\"]\n", "untrusted_content.lockdown.web_fetch_allowed_domains"},
		{"untrusted_content:\n  lockdown:\n    web_fetch_allowed_domains: [\"bad host\"]\n", "untrusted_content.lockdown.web_fetch_allowed_domains"},
	} {
		_, _, err := loadYAML(t, c.body)
		var ie *InvalidError
		if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want InvalidError mentioning %q", c.body, err, c.want)
		}
	}
	// Unknown keys are rejected, so a typo can't silently leave lockdown off.
	for _, body := range []string{
		"untrusted_content:\n  enable: true\n",
		"untrusted_content:\n  lockdown:\n    allowed_domains: [x.com]\n",
	} {
		if _, _, err := loadYAML(t, body); err == nil {
			t.Errorf("%q loaded", body)
		}
	}
}
