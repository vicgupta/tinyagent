package tools

import (
	"reflect"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestCommandEnvBuild(t *testing.T) {
	parent := []string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "FOO=bar", "OPENAI_API_KEY=sk", "MY_SERVICE_TOKEN=t",
		"DB_PASSWORD=p", "AWS_REGION=r", "GITHUB_TOKEN=g", "KEYCHAIN_PATH=k"}
	cases := []struct {
		name     string
		p        config.CommandEnv
		windows  bool
		vars     []string
		withheld []string
		missing  []string
	}{
		{"all", config.CommandEnv{Inherit: "all"}, false, parent, nil, nil},
		{"none", config.CommandEnv{Inherit: "none"}, false, []string{}, []string{"AWS_REGION", "DB_PASSWORD", "FOO", "GITHUB_TOKEN", "HOME", "KEYCHAIN_PATH", "LC_ALL", "MY_SERVICE_TOKEN", "OPENAI_API_KEY", "PATH"}, nil},
		{"core", config.CommandEnv{Inherit: "core"}, false, []string{"PATH=/bin", "HOME=/h", "LC_ALL=C"}, []string{"AWS_REGION", "DB_PASSWORD", "FOO", "GITHUB_TOKEN", "KEYCHAIN_PATH", "MY_SERVICE_TOKEN", "OPENAI_API_KEY"}, nil},
		{"secrets", config.CommandEnv{ExcludeSecrets: true}, false,
			[]string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "FOO=bar", "AWS_REGION=r", "KEYCHAIN_PATH=k"},
			[]string{"DB_PASSWORD", "GITHUB_TOKEN", "MY_SERVICE_TOKEN", "OPENAI_API_KEY"}, nil},
		{"exclude pass set", config.CommandEnv{ExcludeSecrets: true, Exclude: []string{"AWS_*", "FOO"}, Pass: []string{"GITHUB_TOKEN", "FOO", "NOT_SET_ANYWHERE"}, Set: map[string]string{"CI": "true", "HOME": "/x"}}, false,
			[]string{"PATH=/bin", "LC_ALL=C", "FOO=bar", "GITHUB_TOKEN=g", "KEYCHAIN_PATH=k", "CI=true", "HOME=/x"},
			[]string{"AWS_REGION", "DB_PASSWORD", "MY_SERVICE_TOKEN", "OPENAI_API_KEY"}, []string{"NOT_SET_ANYWHERE"}},
		{"windows folds case", config.CommandEnv{Inherit: "none", Pass: []string{"path", "foo"}}, true,
			[]string{"PATH=/bin", "FOO=bar"}, nil, nil},
		{"unix keeps case", config.CommandEnv{Inherit: "none", Pass: []string{"path"}}, false,
			[]string{}, nil, []string{"path"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ce := newCommandEnv(c.p, parent, c.windows)
			if !reflect.DeepEqual(ce.Vars, c.vars) {
				t.Errorf("vars = %q, want %q", ce.Vars, c.vars)
			}
			if c.withheld != nil && !reflect.DeepEqual(ce.Withheld, c.withheld) {
				t.Errorf("withheld = %q, want %q", ce.Withheld, c.withheld)
			}
			if !reflect.DeepEqual(ce.Missing, c.missing) {
				t.Errorf("missing = %q, want %q", ce.Missing, c.missing)
			}
			if len(ce.Passed)+len(ce.Withheld) < len(c.vars) {
				t.Errorf("passed %q withheld %q", ce.Passed, ce.Withheld)
			}
		})
	}
	ce := newCommandEnv(config.CommandEnv{Inherit: "core"}, []string{"SystemRoot=C:\\Windows", "Path=C:\\bin", "X=1"}, true)
	if !reflect.DeepEqual(ce.Vars, []string{"SystemRoot=C:\\Windows", "Path=C:\\bin"}) {
		t.Errorf("windows core = %q", ce.Vars)
	}
	if got := ce.Describe(); got != "command env: core; 2 passed, 1 withheld" {
		t.Errorf("Describe = %q", got)
	}
}
