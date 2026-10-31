package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README and CONTRIBUTING both describe the base tool catalog as
// "<N> base tools (`a`, `b`, ...)". These tests keep that prose in sync with
// Specs(false) and keep CONTRIBUTING's direct-dependency list in sync with
// go.mod, so the docs cannot silently drift from the code (issue #313).

var (
	baseToolsRE = regexp.MustCompile(`(?s)\b([a-z]+) base tools \(([^)]*)\)`)
	backtickRE  = regexp.MustCompile("`([^`]+)`")
	numberWords = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen", "twenty"}
)

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestDocsBaseToolListMatchesSpecs(t *testing.T) {
	var want []string
	for _, s := range Specs(false) {
		want = append(want, s.Function.Name)
	}
	if len(want) >= len(numberWords) {
		t.Fatalf("add more number words: %d base tools", len(want))
	}
	for _, doc := range []string{"README.md", "CONTRIBUTING.md"} {
		text := readRepoFile(t, doc)
		matches := baseToolsRE.FindAllStringSubmatch(text, -1)
		if len(matches) == 0 {
			t.Errorf("%s: no \"<N> base tools (`...`)\" list found; keep one in sync with tools.Specs(false)", doc)
			continue
		}
		for _, m := range matches {
			if m[1] != numberWords[len(want)] {
				t.Errorf("%s: says %q base tools, want %q", doc, m[1], numberWords[len(want)])
			}
			var got []string
			for _, b := range backtickRE.FindAllStringSubmatch(m[2], -1) {
				got = append(got, b[1])
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s: base tool list\n got  %v\n want %v (tools.Specs(false))", doc, got, want)
			}
		}
	}
}

func TestContributingDirectDepsMatchGoMod(t *testing.T) {
	gomod := readRepoFile(t, "go.mod")
	var want []string
	inBlock := false
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		}
		var mod string
		if inBlock {
			mod = line
		} else if rest, ok := strings.CutPrefix(line, "require "); ok {
			mod = rest
		} else {
			continue
		}
		if mod == "" || strings.HasPrefix(mod, "//") || strings.Contains(mod, "// indirect") {
			continue
		}
		want = append(want, strings.Fields(mod)[0])
	}

	text := readRepoFile(t, "CONTRIBUTING.md")
	start := strings.Index(text, "<!-- direct-deps:start -->")
	end := strings.Index(text, "<!-- direct-deps:end -->")
	if start < 0 || end < start {
		t.Fatal("CONTRIBUTING.md: direct-deps markers not found")
	}
	var got []string
	for _, line := range strings.Split(text[start:end], "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "- ") {
			continue
		}
		if m := backtickRE.FindStringSubmatch(line); m != nil {
			got = append(got, m[1])
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("CONTRIBUTING.md direct dependencies\n got  %v\n want %v (go.mod)", got, want)
	}
}
