package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

// starterBlock returns the starter config's commented block that starts
// with "# <key>:", uncommented.
func starterBlock(t *testing.T, key string) string {
	t.Helper()
	var out []string
	in := false
	for _, l := range strings.Split(starterConfig, "\n") {
		if strings.HasPrefix(l, "# "+key+":") {
			in = true
		} else if in && !strings.HasPrefix(l, "#   ") {
			in = false
		}
		if in {
			out = append(out, strings.TrimPrefix(l, "# "))
		}
	}
	if len(out) == 0 {
		t.Fatalf("starter config has no %s block", key)
	}
	return strings.Join(out, "\n") + "\n"
}

// The starter config's untrusted_content block (#171) parses, is off as
// written, and is valid when switched on.
func TestStarterConfigUntrustedContentBlockParses(t *testing.T) {
	block := starterBlock(t, "untrusted_content")
	for _, c := range []struct {
		body    string
		enabled bool
	}{
		{block, false},
		{strings.Replace(block, "enabled: false", "enabled: true", 1), true},
	} {
		p := filepath.Join(t.TempDir(), "c.yaml")
		if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadProfile(p, "")
		if err != nil {
			t.Fatalf("uncommented untrusted_content block: %v\n%s", err, c.body)
		}
		u := cfg.UntrustedContent
		if u.Enabled != c.enabled || u.Sources != nil || len(u.Lockdown.WebFetchAllowedDomains) != 0 {
			t.Errorf("untrusted_content = %+v, want enabled %v with defaults", u, c.enabled)
		}
	}
}
