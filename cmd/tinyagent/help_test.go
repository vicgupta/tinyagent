package main

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestDefaultHelpIsBrief checks plain --help stays short and scannable: a
// usage line first, a Subcommands section, and pointers to --help --verbose
// and docs/subcommands.md last.
func TestDefaultHelpIsBrief(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		code, got, _ := runWith(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d", args, code)
		}
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) > 40 {
			t.Errorf("%v: %d lines, want <= 40:\n%s", args, len(lines), got)
		}
		for _, l := range lines {
			if len(l) > helpWidth {
				t.Errorf("%v: line longer than %d: %q", args, helpWidth, l)
			}
		}
		if !strings.HasPrefix(lines[0], "Usage: tinyagent") || !strings.Contains(got, "\nSubcommands:\n") {
			t.Errorf("%v: want usage first and a Subcommands section:\n%s", args, got)
		}
		if tail := strings.Join(lines[len(lines)-2:], " "); !strings.Contains(tail, "--help --verbose") || !strings.Contains(tail, "docs/subcommands.md") {
			t.Errorf("%v: want pointers to --help --verbose and docs/subcommands.md last:\n%s", args, got)
		}
	}
}

// TestBriefHelpShowsEssentials checks the brief help shows how to pass the
// task (including stdin), the model/provider and its default, and the
// common options, and that every flag it names is a real flag.
func TestBriefHelpShowsEssentials(t *testing.T) {
	_, got, _ := runWith(t, "--help")
	for _, want := range []string{
		"Examples:\n", "Common options:\n", "-t, --task", `"-" reads it from stdin`,
		"-m, --model", "openai, anthropic, openrouter or ollama", "$AGENT_MODEL", builtinModel,
		"-c, --config", "--cwd", "--resume", "--read-only", "-o, --output", "--json",
		"--check", "--update", "-V, --version",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief help lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Common options:") > strings.Index(got, "Subcommands:") {
		t.Errorf("want Common options before Subcommands:\n%s", got)
	}
	fs := newFlagSet(&options{})
	for _, name := range regexp.MustCompile(`(?:^|[\s|(/])--?([a-zA-Z][a-z-]*)`).FindAllStringSubmatch(helpEssentials, -1) {
		if fs.Lookup(name[1]) == nil {
			t.Errorf("brief help names unknown flag -%s", name[1])
		}
	}
}

// TestHelpListsEverySubcommand checks --help names every registered
// subcommand (including tinygo) on its own line with a description, and no
// subcommand that doesn't exist.
func TestHelpListsEverySubcommand(t *testing.T) {
	_, got, _ := runWith(t, "--help")
	sec := got[strings.Index(got, "Subcommands:\n")+len("Subcommands:\n"):]
	sec = sec[:strings.Index(sec, "\n\n")]
	var listed []string
	for _, l := range strings.Split(sec, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			t.Errorf("subcommand line without a description: %q", l)
			continue
		}
		listed = append(listed, f[0])
		if findSubcommand(f[0]) == nil {
			t.Errorf("--help lists unknown subcommand %q", f[0])
		}
	}
	var want []string
	for _, s := range subcommands {
		want = append(want, s.name)
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("--help subcommands = %v, want %v", listed, want)
	}
	for _, name := range []string{"sandbox", "memory", "planner", "plugins", "tinygo"} {
		if findSubcommand(name) == nil {
			t.Errorf("subcommand %q not registered", name)
		}
	}
}

// TestHelpGroupsNameRealFlags checks every flag named in a help section is
// registered and listed once, so help never describes a flag that doesn't
// exist.
func TestHelpGroupsNameRealFlags(t *testing.T) {
	fs := newFlagSet(&options{})
	seen := map[string]bool{}
	for _, g := range verboseGroups() {
		for _, f := range g.flags {
			if fs.Lookup(f[1]) == nil {
				t.Errorf("group %q lists --%s, which isn't a flag", g.name, f[1])
			}
			if f[0] != "" && fs.Lookup(f[0]) == nil {
				t.Errorf("group %q lists -%s, which isn't a flag", g.name, f[0])
			}
			if seen[f[1]] {
				t.Errorf("--%s listed twice", f[1])
			}
			seen[f[1]] = true
		}
	}
	for _, s := range subcommands {
		for name, a := range s.actions {
			if fs.Lookup(a.flag) == nil {
				t.Errorf("%s %s maps to missing flag --%s", s.name, name, a.flag)
			}
		}
		for name, fl := range s.options {
			if fs.Lookup(fl) == nil {
				t.Errorf("%s --%s maps to missing flag --%s", s.name, name, fl)
			}
		}
	}
}

// TestVerboseHelp checks --help --verbose / -h -v: every flag once, grouped
// under headings, sorted within each group, one blank line between groups,
// one description column per group and wrapped lines indented two spaces.
func TestVerboseHelp(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	_, got, _ := runWith(t, "--help", "--verbose")
	if _, short, _ := runWith(t, "-h", "-v"); short != got {
		t.Errorf("-h -v differs from --help --verbose")
	}
	if !strings.HasPrefix(got, helpHeader+"\n") || !strings.HasSuffix(got, "\n\n"+helpFooter) {
		t.Errorf("verbose help doesn't start with the header and end with the footer:\n%s", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Error("more than one blank line between sections")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, helpHeader+"\n"), "\n"+helpFooter)
	groups := verboseGroups()
	blocks := strings.Split(strings.TrimSuffix(body, "\n"), "\n\n")
	if len(blocks) != len(groups) {
		t.Fatalf("%d groups printed, want %d", len(blocks), len(groups))
	}
	seen := map[string]bool{}
	for i, block := range blocks {
		lines := strings.Split(block, "\n")
		if g := groups[i]; g.name != "" {
			if lines[0] != g.name+":" {
				t.Errorf("group %d heading = %q, want %q", i, lines[0], g.name+":")
			}
			lines = lines[1:]
		}
		var names []string
		col := -1
		for _, line := range lines {
			if !strings.HasPrefix(line, "  -") && !strings.HasPrefix(line, "      --") {
				// A wrapped line: two spaces past the description column.
				if col < 0 || !strings.HasPrefix(line, strings.Repeat(" ", col+2)) || line[col+2] == ' ' {
					t.Errorf("wrapped line not indented to column %d: %q", col+2, line)
				}
				continue
			}
			i := strings.Index(line, "--")
			name := strings.Fields(line[i+2:])[0]
			names = append(names, name)
			seen[name] = true
			j := i + strings.Index(line[i:], "   ")
			for j < len(line) && line[j] == ' ' {
				j++
			}
			if col == -1 {
				col = j
			} else if j != col {
				t.Errorf("description at column %d, want %d: %q", j, col, line)
			}
		}
		if !sort.StringsAreSorted(names) {
			t.Errorf("group %d not sorted: %v", i, names)
		}
	}
	var o options
	newFlagSet(&o).VisitAll(func(f *flag.Flag) {
		if len(f.Name) > 1 && !seen[f.Name] {
			t.Errorf("--%s missing from verbose help", f.Name)
		}
	})
	if !strings.Contains(got, "      --cwd dir") || !strings.Contains(got, `(default: ".")`) {
		t.Error("defaults not shown")
	}
}

// TestVerboseHelpGolden pins the exact layout of one group.
func TestVerboseHelpGolden(t *testing.T) {
	_, got, _ := runWith(t, "--help", "--verbose")
	want := `Rewind:
      --dry-run       with --rewind: print what would change and write nothing
      --rewind path   restore --cwd to a workspace snapshot recorded in this trajectory JSON (see
                        --to and --dry-run)
      --to target     with --rewind: start (the baseline, the default), a step number (the state
                        before that step's tool batch), or a snapshot id
`
	if !strings.Contains(got, "\n"+want+"\n") {
		t.Errorf("Rewind group:\n%s\nwant:\n%s", got, want)
	}
}

func TestWrapWords(t *testing.T) {
	got := wrapWords("aaa bbb ccc ddd eee", 8)
	want := []string{"aaa bbb", "ccc", "ddd", "eee"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrapWords = %q, want %q", got, want)
	}
}

func TestTruncateList(t *testing.T) {
	list := func(n int) []string {
		var l []string
		for i := range n {
			l = append(l, fmt.Sprintf("line %d", i))
		}
		return l
	}
	if got := truncateList(list(10)); len(got) != 10 {
		t.Errorf("10 lines truncated to %d", len(got))
	}
	got := truncateList(list(12))
	want := append(list(5), "…and 7 more. Run tinyagent --help --verbose to see all.")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("truncateList(12) = %q, want %q", got, want)
	}
}

// TestVerboseWithoutHelpUnchanged: -v / --verbose alone still only turns on
// tool output; they don't print help.
func TestVerboseWithoutHelpUnchanged(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	for _, v := range []string{"-v", "--verbose"} {
		var o options
		if err := newFlagSet(&o).Parse([]string{v}); err != nil {
			t.Fatal(err)
		}
		if !o.verbose || o.showHelp {
			t.Errorf("%s: verbose = %v, showHelp = %v", v, o.verbose, o.showHelp)
		}
		code, out, errOut := runWith(t, v, "stray")
		wantCode, wantOut, wantErr := runWith(t, "stray")
		if code != wantCode || out != wantOut || errOut != wantErr {
			t.Errorf("%s changed a normal run: exit %d, stdout %q, stderr %q", v, code, out, errOut)
		}
		if strings.Contains(out, helpBrief) {
			t.Errorf("%s printed help", v)
		}
	}
}

// TestNoArgsPrintsBriefHelp checks plain "tinyagent" at a terminal prints
// exactly the --help output and exits 0, while with stdin piped it still
// fails with "--task is required" (exit 2) as before.
func TestNoArgsPrintsBriefHelp(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	defer func(f func() bool) { stdinIsTerminal = f }(stdinIsTerminal)
	stdinIsTerminal = func() bool { return true }
	code, got, errOut := runWith(t)
	_, want, _ := runWith(t, "--help")
	if code != 0 || got != want || errOut != "" {
		t.Errorf("no args at a terminal: exit %d, stderr %q, stdout:\n%s\nwant --help output:\n%s", code, errOut, got, want)
	}
	// Any argument keeps the normal path.
	if code, out, _ := runWith(t, "--cwd", "."); code != 2 || out == want {
		t.Errorf("--cwd alone: exit %d, want 2 without help on stdout", code)
	}
	stdinIsTerminal = func() bool { return false }
	if code, out, _ := runWith(t); code != 2 || out != "" {
		t.Errorf("no args with piped stdin: exit %d, stdout %q; want exit 2 and no help", code, out)
	}
}

// TestIsTerminalFalseForNonTTY checks a pipe and /dev/null (a character
// device, but not a terminal) don't count as a terminal.
func TestIsTerminalFalseForNonTTY(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) {
		t.Error("pipe reported as a terminal")
	}
	if f, err := os.Open(os.DevNull); err == nil {
		defer f.Close()
		if isTerminal(f) {
			t.Error(os.DevNull + " reported as a terminal")
		}
	}
}
