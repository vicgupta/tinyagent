package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// TestCompletionValuesMatchAccepted guards against drift between the value
// table and what each flag accepts: every listed value is accepted, every
// other known word is rejected, and --help names each value.
func TestCompletionValuesMatchAccepted(t *testing.T) {
	accepts := map[string]func(string) bool{
		"command-env":      func(v string) bool { return v != "" && config.ValidInherit(v) },
		"env":              func(v string) bool { return v != "" && config.ValidEnvironment(v) },
		"openai-api":       model.ValidOpenAIAPI,
		"reasoning-effort": model.ValidReasoningEffort,
		"sandbox-backend":  func(v string) bool { return v != "" && config.ValidSandboxBackend(v) },
		"completion": func(v string) bool {
			return v != "" && runCompletionQuiet(v) == 0
		},
	}
	if got, want := completionValueFlags(), []string{"command-env", "completion", "env", "openai-api", "reasoning-effort", "sandbox-backend"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("value flags = %v, want %v", got, want)
	}
	var universe []string
	for _, vals := range completionValues {
		universe = append(universe, vals...)
	}
	universe = append(universe, "powershell", "default", "anthropic", "ultra", "remote", "secrets")
	fs := newFlagSet(&options{})
	for _, name := range completionValueFlags() {
		accept, ok := accepts[name]
		if !ok {
			t.Fatalf("no acceptance check for --%s", name)
		}
		vals := completionValues[name]
		for _, v := range universe {
			if got, want := accept(v), slices.Contains(vals, v); got != want {
				t.Errorf("--%s %q: accepted %v, but listed for completion %v", name, v, got, want)
			}
		}
		f := fs.Lookup(name)
		if f == nil {
			t.Fatalf("--%s is not a registered flag", name)
		}
		for _, v := range vals {
			if !strings.Contains(f.Usage, v) {
				t.Errorf("--%s help %q doesn't name %q", name, f.Usage, v)
			}
		}
	}
}

// runCompletionQuiet is runCompletion with the script and any error message
// discarded.
func runCompletionQuiet(shell string) int {
	stderr := os.Stderr
	devnull, err := os.Open(os.DevNull)
	if err == nil {
		os.Stderr = devnull
		defer func() { os.Stderr = stderr; devnull.Close() }()
	}
	return runCompletion(shell, newFlagSet(&options{}), io.Discard)
}

// TestCompletionValuesInScripts checks that every generated script embeds
// each value flag's values.
func TestCompletionValuesInScripts(t *testing.T) {
	scripts := map[string]string{}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		scripts[shell], _ = shellCompletionScript(t, shell)
	}
	for _, name := range completionValueFlags() {
		vals := strings.Join(completionValues[name], " ")
		for shell, want := range map[string]string{
			"bash": "-" + name + "|--" + name + ") values='" + vals + "' ;;",
			"zsh":  "(-" + name + "|--" + name + ") values=(" + vals + ") ;;",
			"fish": "complete -c tinyagent -l " + name + " -x -a '" + vals + "'\n",
		} {
			if !strings.Contains(scripts[shell], want) {
				t.Errorf("%s script lacks %q", shell, want)
			}
		}
	}
}

func TestCompletionBashValues(t *testing.T) {
	_, script := completionScript(t)
	tests := []struct {
		words []string
		want  []string
	}{
		{[]string{"--env", ""}, []string{"local", "container"}},
		{[]string{"--env", "co"}, []string{"container"}},
		{[]string{"-env", "l"}, []string{"local"}},
		{[]string{"--env", "=", ""}, []string{"local", "container"}},
		{[]string{"--env", "="}, []string{"local", "container"}},
		{[]string{"--env", "=", "lo"}, []string{"local"}},
		{[]string{"--env=co"}, []string{"--env=container"}},
		{[]string{"--command-env", ""}, []string{"all", "core", "none"}},
		{[]string{"--reasoning-effort", "x"}, []string{"xhigh"}},
		{[]string{"--openai-api", "r"}, []string{"responses"}},
		{[]string{"--completion", "f"}, []string{"fish"}},
		{[]string{"--env", "--verb"}, []string{"--verbose"}},
		{[]string{"--env", "nope"}, nil},
		{[]string{"--model", ""}, nil},
		{[]string{"-t", "do it", "lo"}, nil},
	}
	for _, tc := range tests {
		if got := bashComplete(t, script, tc.words...); !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("complete %q = %q, want %q", tc.words, got, tc.want)
		}
	}
}

func TestCompletionZshValues(t *testing.T) {
	_, script := shellCompletionScript(t, "zsh")
	zshPath(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		line string
		want []string
	}{
		{"tinyagent --env ", []string{"local", "container"}},
		{"tinyagent --env co", []string{"container"}},
		{"tinyagent --env=co", []string{"container"}},
		{"tinyagent --reasoning-effort x", []string{"xhigh"}},
		{"tinyagent --env --verb", []string{"--verbose"}},
		{"tinyagent --model oth", []string{"other.txt"}},
	}
	var lines []string
	for _, tc := range tests {
		lines = append(lines, tc.line)
	}
	_, got := zshComplete(t, script, dir, lines...)
	for i, tc := range tests {
		if !reflect.DeepEqual(got[i], tc.want) {
			t.Errorf("complete %q = %q, want %q", tc.line, got[i], tc.want)
		}
	}
}

func TestCompletionFishValues(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil || runtime.GOOS == "windows" {
		if os.Getenv("TINYAGENT_REQUIRE_FISH") != "" {
			t.Fatalf("TINYAGENT_REQUIRE_FISH is set but fish is unavailable: %v", err)
		}
		t.Skip("needs fish")
	}
	_, script := shellCompletionScript(t, "fish")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"tinyagent --env ", []string{"container", "local"}},
		{"tinyagent --env co", []string{"container"}},
		{"tinyagent --env=co", []string{"--env=container"}},
		{"tinyagent --command-env ", []string{"all", "core", "none"}},
		{"tinyagent --reasoning-effort x", []string{"xhigh"}},
		{"tinyagent --openai-api ", []string{"auto", "chat", "responses"}},
		{"tinyagent --completion ", []string{"bash", "fish", "zsh"}},
		{"tinyagent --model oth", []string{"other.txt"}},
		{"tinyagent oth", []string{"other.txt"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, fish, "--no-config", "-c", `set -g fish_complete_path; source $argv[1]; complete -C $argv[2]`, script, tc.line)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+t.TempDir(), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("fish completing %q: %v", tc.line, err)
			}
			var got []string
			for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
				if line != "" {
					candidate, _, _ := strings.Cut(line, "\t")
					got = append(got, candidate)
				}
			}
			slices.Sort(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("candidates = %q, want %q", got, tc.want)
			}
		})
	}
}
