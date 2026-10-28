package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestUntrustedLockdownAllowedDomains(t *testing.T) {
	cfg, _, err := loadYAML(t, "untrusted_content:\n  enabled: true\n  lockdown:\n    allowed_domains: [proxy.golang.org, \"*.golang.org\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"proxy.golang.org", "*.golang.org"}; !reflect.DeepEqual(cfg.UntrustedContent.Lockdown.AllowedDomains, want) {
		t.Errorf("allowed_domains = %v", cfg.UntrustedContent.Lockdown.AllowedDomains)
	}
	for _, body := range []string{
		"untrusted_content:\n  lockdown:\n    allowed_domains: [\"*\"]\n",
		"untrusted_content:\n  lockdown:\n    allowed_domains: [\"bad host\"]\n",
	} {
		_, _, err := loadYAML(t, body)
		var ie *InvalidError
		if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), "untrusted_content.lockdown.allowed_domains") {
			t.Errorf("%q: err = %v", body, err)
		}
	}
}
