package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// completionScript generates the Bash script through the CLI and writes it to
// a file, returning the stdout text and the file path.
func completionScript(t *testing.T) (string, string) {
	t.Helper()
	return shellCompletionScript(t, "bash")
}

// shellCompletionScript is completionScript for any supported shell.
func shellCompletionScript(t *testing.T, shell string) (string, string) {
	t.Helper()
	code, stdout, stderr := runCLI(t, "--completion", shell)
	if code != 0 || stderr != "" {
		t.Fatalf("--completion %s: exit %d, stderr:\n%s", shell, code, stderr)
	}
	p := filepath.Join(t.TempDir(), "tinyagent."+shell)
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
	testCompletionOfflineWithBrokenConfig(t, "bash", "# Bash completion for tinyagent", "complete -o default -F _tinyagent tinyagent\n")
}

func TestCompletionZshOfflineWithBrokenConfig(t *testing.T) {
	testCompletionOfflineWithBrokenConfig(t, "zsh", "#compdef tinyagent\n# Zsh completion for tinyagent", "compdef _tinyagent tinyagent\n")
}

// testCompletionOfflineWithBrokenConfig checks that --completion shell prints
// only the script, ignoring a broken config, task stdin and output flags.
func testCompletionOfflineWithBrokenConfig(t *testing.T, shell, prefix, register string) {
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
	code, stdout, stderr := runCLI(t, "--completion", shell, "-t", "-", "-o", "run.json", "--log-db")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, prefix) || !strings.HasSuffix(stdout, register) {
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
	for _, shell := range []string{"fish", ""} {
		code, stdout, stderr := runCLI(t, "--completion", shell)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "supported shells are bash and zsh") {
			t.Errorf("--completion %q: exit %d, stdout %q, stderr %q; want exit 2 naming bash and zsh", shell, code, stdout, stderr)
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

// zshPath returns the zsh to test with, skipping when there is none unless
// TINYAGENT_REQUIRE_ZSH is set, so a CI job that installs zsh can't skip.
func zshPath(t *testing.T) string {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil || runtime.GOOS == "windows" {
		if os.Getenv("TINYAGENT_REQUIRE_ZSH") != "" {
			t.Fatalf("TINYAGENT_REQUIRE_ZSH is set but zsh is unavailable: %v", err)
		}
		t.Skip("needs zsh")
	}
	return zsh
}

// zshHarness drives an interactive zsh -f through a zpty pseudo-terminal:
// it runs compinit, sources the script, wraps compadd to record the matches
// each completion offers, then types every line followed by Tab. zle-line-init
// prints a marker so each line is typed only once the editor is ready, so
// every command sent is a single line.
const zshHarness = `
script=$1 out=$2; shift 2
zmodload zsh/zpty || exit 1
zpty z zsh -f -i || exit 1
ready() { zpty -r z _ '*<RDY>*' || exit 1 }
zpty -w z "PS1='' PROMPT='' RPROMPT=''; zle-line-init() { print -n '<R''DY>' }; zle -N zle-line-init"
ready
zpty -w z "autoload -Uz compinit && compinit -u -d ${(q)out}/zcompdump && source ${(q)script} && print -r -- \${_comps[tinyagent]} \$+functions[complete] > ${(q)out}/registered"
ready
zpty -w z 'compadd() { if [[ ${@[1,(i)(-|--)]} == *-(O|A|D)\ * ]]; then builtin compadd "$@"; return; fi; local -a hits; builtin compadd -O hits "$@"; (( $#hits )) && print -rl -- $hits >> $COMPOUT; builtin compadd "$@" }'
ready
i=0
for line; do
  (( i++ ))
  zpty -w z "COMPOUT=${(q)out}/$i; : > \$COMPOUT"
  ready
  zpty -wn z "$line"$'\t\C-u\n'
  ready
done
zpty -d z
`

// zshComplete types each command line plus Tab into an interactive zsh in dir
// that has sourced script after compinit, returning the offered matches per
// line and the registration ("_tinyagent 0": the compdef function, and no
// bashcompinit's complete). A tinyagent on PATH records any run.
func zshComplete(t *testing.T, script, dir string, lines ...string) (string, [][]string) {
	t.Helper()
	zsh := zshPath(t)
	out, bin := t.TempDir(), t.TempDir()
	tripwire := filepath.Join(out, "tinyagent-ran")
	if err := os.WriteFile(filepath.Join(bin, "tinyagent"), []byte("#!/bin/sh\n: > '"+tripwire+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, zsh, append([]string{"-f", "-c", zshHarness, "zsh", script, out}, lines...)...)
	cmd.Dir = dir
	home := t.TempDir()
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + filepath.Dir(zsh), "HOME=" + home, "ZDOTDIR=" + home, "TERM=dumb"}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zsh harness: %v\n%s", err, b)
	}
	if _, err := os.Stat(tripwire); err == nil {
		t.Error("pressing Tab ran tinyagent")
	}
	reg, err := os.ReadFile(filepath.Join(out, "registered"))
	if err != nil {
		t.Fatalf("sourcing the script after compinit failed: %v", err)
	}
	var got [][]string
	for i := range lines {
		b, err := os.ReadFile(filepath.Join(out, strconv.Itoa(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		var m []string
		if s := strings.TrimSuffix(string(b), "\n"); s != "" {
			m = strings.Split(s, "\n")
		}
		got = append(got, m)
	}
	return strings.TrimSpace(string(reg)), got
}

// TestCompletionZshFlagsMatchFlagSet guards against drift: the Zsh script
// embeds exactly completionFlags of the registered flag set.
func TestCompletionZshFlagsMatchFlagSet(t *testing.T) {
	stdout, _ := shellCompletionScript(t, "zsh")
	_, rest, _ := strings.Cut(stdout, "flags=(")
	list, _, _ := strings.Cut(rest, ")")
	want := completionFlags(newFlagSet(&options{}))
	if got := strings.Fields(list); !reflect.DeepEqual(got, want) {
		t.Errorf("script flags:\n%v\nwant completionFlags:\n%v", got, want)
	}
	if strings.Contains(stdout, "bashcompinit") || strings.Contains(stdout, "complete ") {
		t.Error("script uses Bash completion emulation")
	}
}

func TestCompletionZshCandidates(t *testing.T) {
	_, script := shellCompletionScript(t, "zsh")
	zsh := zshPath(t)
	if out, err := exec.Command(zsh, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("zsh -n: %v\n%s", err, out)
	}
	dir := t.TempDir()
	for _, name := range []string{"my file.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		line string
		want []string
	}{
		{"tinyagent --tool-out", []string{"--tool-output-files"}},
		{"tinyagent --sandbox-", []string{"--sandbox-allow-domain", "--sandbox-network"}},
		{"tinyagent -v --sandbox", []string{"--sandbox", "--sandbox-allow-domain", "--sandbox-network"}},
		{"tinyagent -t", []string{"-t"}},
		{"tinyagent --he", []string{"--help"}},
		{"tinyagent --nope-", nil},
		{`tinyagent -t "do it" my`, []string{`my\ file.txt`}},
		{"tinyagent --cwd . oth", []string{"other.txt"}},
	}
	var lines []string
	for _, tc := range tests {
		lines = append(lines, tc.line)
	}
	reg, got := zshComplete(t, script, dir, lines...)
	if reg != "_tinyagent 0" {
		t.Errorf("registration = %q, want compdef's _tinyagent without bashcompinit", reg)
	}
	for i, tc := range tests {
		if !reflect.DeepEqual(got[i], tc.want) {
			t.Errorf("complete %q = %q, want %q", tc.line, got[i], tc.want)
		}
	}
}

func TestCompletionZshNeedsCompinit(t *testing.T) {
	_, script := shellCompletionScript(t, "zsh")
	out, err := exec.Command(zshPath(t), "-f", "-c", `source "$1"`, "zsh", script).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "compinit") {
		t.Errorf("sourcing without compinit: %v, output %q; want a failure naming compinit", err, out)
	}
}
