package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWebFetchConfig(t *testing.T) {
	d := Default().WebFetch
	if d.Enabled || len(d.AllowedDomains) != 0 || d.MaxBytes != 2097152 || d.TimeoutSeconds != 20 {
		t.Fatalf("defaults = %+v", d)
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "web_fetch:\n  enabled: true\n  allowed_domains: [pkg.go.dev, \"*.python.org\"]\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := WebFetch{Enabled: true, AllowedDomains: []string{"pkg.go.dev", "*.python.org"}, MaxBytes: 2097152, TimeoutSeconds: 20}
	if !reflect.DeepEqual(cfg.WebFetch, want) {
		t.Fatalf("got %+v", cfg.WebFetch)
	}
	for _, c := range []struct{ yaml, want string }{
		{"web_fetch: {max_bytes: 0}", "web_fetch.max_bytes"},
		{"web_fetch: {max_bytes: -1}", "web_fetch.max_bytes"},
		{"web_fetch: {timeout_seconds: 0}", "web_fetch.timeout_seconds"},
		{"web_fetch: {timeout_seconds: 121}", "web_fetch.timeout_seconds"},
		{"web_fetch: {allowed_domains: [\"*\"]}", "web_fetch.allowed_domains"},
		{"web_fetch: {allowed_domains: [\"http://x.com/\"]}", "web_fetch.allowed_domains"},
	} {
		write(t, p, c.yaml+"\n")
		_, err := Load(p)
		var invalid *InvalidError
		if err == nil || !strings.Contains(err.Error(), c.want) || !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want InvalidError naming %q", c.yaml, err, c.want)
		}
	}
	write(t, p, "web_fetch: {bogus: 1}\n")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("unknown key: %v", err)
	}
	write(t, p, "web_fetch: {timeout_seconds: 120, max_bytes: 1}\n")
	if _, err := Load(p); err != nil {
		t.Errorf("limits at the edge: %v", err)
	}
}
