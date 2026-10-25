//go:build !windows

package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
)

// envLines runs env through bash and returns its sorted lines, without the
// variables bash sets itself.
func envLines(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && k != "PWD" && k != "SHLVL" && k != "_" && k != "OLDPWD" {
			m[k] = v
		}
	}
	return m
}

func TestCommandEnvTools(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-sentinel")
	t.Setenv("FOO", "bar")
	t.Setenv("GITHUB_TOKEN", "gh-sentinel")
	t.Setenv("AWS_REGION", "us-east-1")
	root := t.TempDir()
	run := func(env Environment, cmd string) map[string]string {
		t.Helper()
		out, err := bashStream(context.Background(), env, root, cmd, 10*time.Second, nil)
		if err != nil || !strings.Contains(out, "<returncode>0</returncode>") {
			t.Fatalf("%s: %v %s", cmd, err, out)
		}
		return envLines(out)
	}
	envBin := "/usr/bin/env"
	if _, err := os.Stat(envBin); err != nil {
		envBin = "env"
	}

	// No policy: the whole environment, as before.
	if got := run(Local{}, envBin); got["FOO"] != "bar" || got["OPENAI_API_KEY"] != "sk-sentinel" {
		t.Errorf("default env lost variables: %v", got)
	}
	none := Local{CommandEnv: NewCommandEnv(config.CommandEnv{Inherit: "none"}, os.Environ())}
	if got := run(none, envBin); len(got) != 0 {
		t.Errorf("inherit none: %v", got)
	}
	core := Local{CommandEnv: NewCommandEnv(config.CommandEnv{Inherit: "core"}, os.Environ())}
	got := run(core, "env")
	if got["PATH"] != os.Getenv("PATH") || got["HOME"] != os.Getenv("HOME") || got["FOO"] != "" {
		t.Errorf("inherit core: %v", got)
	}
	mixed := Local{CommandEnv: NewCommandEnv(config.CommandEnv{ExcludeSecrets: true, Exclude: []string{"AWS_*"}, Pass: []string{"GITHUB_TOKEN"}, Set: map[string]string{"CI": "true"}}, os.Environ())}
	for _, cmd := range []string{"env", "bash -c 'bash -c env'"} { // the grandchild sees the same
		got := run(mixed, cmd)
		if _, ok := got["AWS_REGION"]; ok || got["OPENAI_API_KEY"] != "" || got["GITHUB_TOKEN"] != "gh-sentinel" || got["CI"] != "true" || got["FOO"] != "bar" {
			t.Errorf("%s: %v", cmd, got)
		}
	}

	// start_process gets the same environment as bash.
	e := &Executor{Root: root, Env: none}
	defer e.Close()
	id, _ := startID(t, e, envBin+"; echo done")
	var out string
	waitFor(t, func() bool {
		out += runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 200})
		return strings.Contains(out, "done")
	})
	if strings.Contains(out, "FOO=") || strings.Contains(out, "PATH=") {
		t.Errorf("start_process with inherit none: %s", out)
	}
}

func TestCommandEnvSandbox(t *testing.T) {
	root := t.TempDir()
	sb := sandboxOrSkip(t, root, nil, false)
	t.Setenv("FOO", "bar")
	env := Local{Sandbox: sb, CommandEnv: NewCommandEnv(config.CommandEnv{Inherit: "none"}, os.Environ())}
	out, err := bashStream(context.Background(), env, root, "/usr/bin/env", 10*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := envLines(out)
	if len(got) != 1 || got["TMPDIR"] != sb.Tmp {
		t.Errorf("sandboxed inherit none: %v\n%s", got, out)
	}
}
