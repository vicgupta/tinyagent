package main

import (
	"flag"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestDefaultHelpIsBrief enforces issue #164's limits on plain --help: at
// most 3 lines and about 200 characters, excluding the command name.
func TestDefaultHelpIsBrief(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		code, got, _ := runWith(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d", args, code)
		}
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) > 3 {
			t.Errorf("%v: %d lines, want <= 3:\n%s", args, len(lines), got)
		}
		if n := len(strings.ReplaceAll(strings.Join(lines, ""), "tinyagent", "")); n > 200 {
			t.Errorf("%v: %d characters excluding the command name, want <= 200", args, n)
		}
		if !strings.HasPrefix(lines[0], "Usage: tinyagent") || !strings.Contains(lines[len(lines)-1], "--help --verbose") {
			t.Errorf("%v: want usage first and a pointer to --help --verbose last:\n%s", args, got)
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
	if !strings.HasPrefix(got, helpBrief+"\n") {
		t.Errorf("verbose help doesn't start with the brief and one blank line:\n%s", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Error("more than one blank line between sections")
	}
	body := strings.TrimPrefix(got, helpBrief+"\n")
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
