package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// completionScript generates the Bash script through the CLI and writes it to
// a file, returning the stdout text and the file path.
func completionScript(t *testing.T) (string, string) {
	t.Helper()
	code, stdout, stderr := runCLI(t, "--completion", "bash")
	if code != 0 || stderr != "" {
		t.Fatalf("--completion bash: exit %d, stderr:\n%s", code, stderr)
	}
	p := filepath.Join(t.TempDir(), "tinyagent.bash")
	if err := os.WriteFile(p, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	return stdout, p
}

// bashComplete sources script in a clean Bash and runs the function
// registered for tinyagent on the command line words, returning COMPREPLY.
func bashComplete(t *testing.T, script string, words ...string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs bash")
	}
	const prog = `
source "$1"; shift
spec=$(complete -p tinyagent)
fn=${spec##*-F }; fn=${fn%% *}
COMP_WORDS=("$@"); COMP_CWORD=$(($# - 1))
"$fn" tinyagent "${COMP_WORDS[COMP_CWORD]}" "${COMP_WORDS[COMP_CWORD-1]}"
printf '%s\n' "${COMPREPLY[@]}"`
	cmd := exec.Command(bash, append([]string{"--norc", "--noprofile", "-c", prog, "bash", script, "tinyagent"}, words...)...)
	cmd.Env = []string{"PATH=" + t.TempDir()} // tinyagent itself is never on PATH
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash completing %q: %v\n%s", words, err, out)
	}
	return strings.Fields(string(out))
}

func TestCompletionBashOfflineWithBrokenConfig(t *testing.T) {
	hits := checkEnv(t)
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tinyagent")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("model: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	code, stdout, stderr := runCLI(t, "--completion", "bash", "-t", "-", "-o", "run.json", "--log-db")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "# Bash completion for tinyagent") || !strings.Contains(stdout, "complete -o default -F _tinyagent tinyagent\n") {
		t.Errorf("stdout is not the completion script:\n%s", stdout)
	}
	if hits.Load() != 0 {
		t.Errorf("%d network requests, want none", hits.Load())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("created %v in the working directory, want nothing", entries)
	}
}

func TestCompletionUnsupportedShell(t *testing.T) {
	for _, shell := range []string{"zsh", ""} {
		code, stdout, stderr := runCLI(t, "--completion", shell)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "only supported shell is bash") {
			t.Errorf("--completion %q: exit %d, stdout %q, stderr %q; want exit 2 naming bash", shell, code, stdout, stderr)
		}
	}
	code, _, stderr := runCLI(t, "--completion")
	if code != 2 || !strings.Contains(stderr, "flag needs an argument: -completion") {
		t.Errorf("--completion without a value: exit %d, stderr %q; want the flag parse error", code, stderr)
	}
}

// TestCompletionFlagsMatchFlagSet guards against drift: the script offers
// exactly the registered flags.
func TestCompletionFlagsMatchFlagSet(t *testing.T) {
	stdout, _ := completionScript(t)
	_, rest, _ := strings.Cut(stdout, "local flags='")
	list, _, _ := strings.Cut(rest, "'")
	var want []string
	newFlagSet(&options{}).VisitAll(func(f *flag.Flag) {
		if len(f.Name) == 1 {
			want = append(want, "-"+f.Name)
		} else {
			want = append(want, "--"+f.Name)
		}
	})
	slices.Sort(want)
	if got := strings.Fields(list); !reflect.DeepEqual(got, want) {
		t.Errorf("script flags:\n%v\nwant the registered flags:\n%v", got, want)
	}
}

func TestCompletionBashCandidates(t *testing.T) {
	stdout, script := completionScript(t)
	if bash, err := exec.LookPath("bash"); err == nil {
		if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
		}
	}
	if !strings.Contains(stdout, "complete -o default ") {
		t.Error("registration doesn't keep the default filename fallback")
	}
	tests := []struct {
		words []string
		want  []string
	}{
		{[]string{"--tool-out"}, []string{"--tool-output-files"}},
		{[]string{"--sandbox-"}, []string{"--sandbox-allow-domain", "--sandbox-network"}},
		{[]string{"-v", "--sandbox"}, []string{"--sandbox", "--sandbox-allow-domain", "--sandbox-network"}},
		{[]string{"-t"}, []string{"-t"}},
		{[]string{"--he"}, []string{"--help"}},
		{[]string{"--nope-"}, nil},
		{[]string{"-t", "do it", "fil"}, nil},
	}
	for _, tc := range tests {
		if got := bashComplete(t, script, tc.words...); !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("complete %q = %q, want %q", tc.words, got, tc.want)
		}
	}
}
