package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestUntrustedContentSources(t *testing.T) {
	if s := Default().UntrustedContent.Sources; s != nil {
		t.Errorf("default sources = %q, want nil (all)", s)
	}
	cfg, _, err := loadYAML(t, "untrusted_content:\n  enabled: true\n  sources: [mcp, review]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.UntrustedContent.Sources; !slices.Equal(got, []string{"mcp", "review"}) {
		t.Errorf("sources = %q", got)
	}
	for _, body := range []string{
		"untrusted_content:\n  sources: []\n",
		"untrusted_content:\n  sources: [webfetch]\n",
		"untrusted_content:\n  sources: [mcp, mcp]\n",
		"untrusted_content:\n  sources: [untrusted_task]\n",
	} {
		_, _, err := loadYAML(t, body)
		var ie *InvalidError
		if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), "untrusted_content.sources") {
			t.Errorf("%q: err = %v, want InvalidError naming untrusted_content.sources", body, err)
		}
	}
}
