package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRedactSecretsConfig(t *testing.T) {
	if !reflect.DeepEqual(Default().RedactSecrets, RedactSecrets{}) {
		t.Error("redact_secrets is on by default")
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "redact_secrets:\n  enabled: true\n  env: [DATABASE_URL, \"DEPLOY_*\"]\n  allow: [PUBLIC_KEY]\n  patterns: true\n")
	c, err := load(t, p)
	if err != nil {
		t.Fatal(err)
	}
	want := RedactSecrets{Enabled: true, Env: []string{"DATABASE_URL", "DEPLOY_*"}, Allow: []string{"PUBLIC_KEY"}, Patterns: true}
	if !reflect.DeepEqual(c.RedactSecrets, want) {
		t.Errorf("redact_secrets = %+v", c.RedactSecrets)
	}
	for _, bad := range []struct{ yaml, want string }{
		{"  enabled: true\n  mask: [x]\n", "mask"},
		{"  env: [\"\"]\n", "redact_secrets.env"},
		{"  allow: [\"[\"]\n", "redact_secrets.allow"},
		{"  env: [X_URL]\n  allow: [X_URL]\n", "both env and allow"},
		{"  patterns: sometimes\n", "redact_secrets"},
		{"  patterns: yes\n", "redact_secrets.patterns must be true or false"},
		{"  enabled: 1\n", "redact_secrets"},
	} {
		write(t, p, "redact_secrets:\n"+bad.yaml)
		_, err := load(t, p)
		var invalid *InvalidError
		if !errors.As(err, &invalid) || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: err = %v", bad.yaml, err)
		}
	}
}
