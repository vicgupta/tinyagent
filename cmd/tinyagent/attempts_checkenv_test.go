package main

import (
	"bytes"
	"context"
	"flag"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func TestAttemptCheckPlace(t *testing.T) {
	cases := []struct {
		cfg  config.AgentConfig
		want string
	}{
		{config.AgentConfig{}, "host"},
		{config.AgentConfig{CommandEnv: config.CommandEnv{Inherit: "core"}}, "command_env"},
		{config.AgentConfig{Sandbox: true, CommandEnv: config.CommandEnv{Inherit: "core"}}, "sandbox"},
		{config.AgentConfig{Environment: "container", CommandEnv: config.CommandEnv{Inherit: "core"}}, "container"},
	}
	for _, c := range cases {
		if got := attemptCheckPlace(c.cfg); got != c.want {
			t.Errorf("attemptCheckPlace(%+v) = %q, want %q", c.cfg, got, c.want)
		}
	}
}

func TestAttemptCheckEnvHost(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	if attemptCheckEnv(fs, options{}, config.AgentConfig{}, &bytes.Buffer{}) != nil {
		t.Fatal("unconfined host run: want nil (checks keep tinyagent's environment)")
	}
}

func TestAttemptCheckEnvCommandEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checks run through bash")
	}
	t.Setenv("TA_ATTEMPT_WITHHELD", "leak")
	cfg := config.AgentConfig{CommandEnv: config.CommandEnv{Inherit: "core", Set: map[string]string{"TA_ATTEMPT_SET": "ok"}}}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	prep := attemptCheckEnv(fs, options{}, cfg, &bytes.Buffer{})
	root := t.TempDir()
	r, release, err := prep(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out, err := r.Command(context.Background(), root, `echo "set=$TA_ATTEMPT_SET withheld=$TA_ATTEMPT_WITHHELD"; pwd`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "set=ok withheld=\n") || !strings.Contains(got, root) {
		t.Fatalf("output = %q", got)
	}
}

func TestAttemptCheckEnvContainerFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no container environment on Windows")
	}
	cfg := config.AgentConfig{Environment: "container"}
	cfg.Container.Runtime = "tinyagent-no-such-runtime"
	cfg.Container.Image = "busybox"
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	prep := attemptCheckEnv(fs, options{}, cfg, &bytes.Buffer{})
	if _, _, err := prep(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want the missing runtime (no host fallback)", err)
	}
}

func TestAttemptCheckEnvSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sandbox on Windows")
	}
	cfg := config.AgentConfig{Sandbox: true}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	prep := attemptCheckEnv(fs, options{}, cfg, &bytes.Buffer{})
	root, outside := t.TempDir(), t.TempDir()
	r, release, err := prep(context.Background(), root)
	if err != nil {
		t.Skip("sandbox unavailable:", err)
	}
	defer release()
	if out, err := r.Command(context.Background(), root, "touch inside").CombinedOutput(); err != nil {
		t.Fatalf("write in the worktree: %v: %s", err, out)
	}
	if err := r.Command(context.Background(), root, "touch "+outside+"/escaped").Run(); err == nil {
		t.Fatal("sandboxed check wrote outside the attempt's worktree")
	}
}
