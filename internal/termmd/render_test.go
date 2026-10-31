package termmd

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// cases covers each element the renderer knows.
var cases = map[string]string{
	"heading":    "# Title\n\n## Section\n\n### Small\n\ntext\n",
	"emphasis":   "Some **bold**, *italic*, ~~gone~~ and `code` and **`both`**. Not*emphasis*here, 2 * 3 * 4, snake_case_name, \\*escaped\\*.\n",
	"wrap":       "This paragraph is long enough that it has to wrap at a narrow width, and it keeps every single word in the right order without losing any text at all.\n",
	"lists":      "- one\n- two\n  - nested **a**\n    - deeper\n- three\n\n1. first\n2. second\n10. tenth\n\n- [ ] todo\n- [x] done\n",
	"quote":      "> quoted line\n> with **bold**\n>\n> > nested\n\nafter\n",
	"code":       "```go\nfunc main() {\n\t// say hi\n\tfmt.Println(\"hi\", 42)\n}\n```\n\n~~~\nno language\n~~~\n",
	"codeothers": "```python\ndef f(x):  # c\n    return None\n```\n\n```sh\necho \"$HOME\" # c\n```\n\n```diff\n- old\n+ new\n```\n",
	"links":      "See [the docs](https://example.com/docs) or <https://example.com> and ![logo](img.png).\n",
	"hr":         "above\n\n---\n\nbelow\n\n***\n",
	"table":      "| Name | Count | Note |\n|:-----|------:|:----:|\n| alpha | 1 | short |\n| beta **b** | 22 | a much longer note that wraps when narrow |\n",
	"cjk":        "中文的段落需要正确地计算宽度并且换行。日本語のテキストも同じです。\n\n| 名前 | 値 |\n|---|---|\n| 東京 | 1 |\n",
	"mixed":      "Intro line\n# Heading right after\n- list right after\nparagraph\n```\nfence right after\n```\n",
	"control":    "bad \x1b[31mescape\x1b[0m, \x1b]0;title\a bell\a and \x1b[2J here\n",
}

func TestGolden(t *testing.T) {
	for name, md := range cases {
		for _, color := range []bool{false, true} {
			for _, width := range []int{30, 80} {
				opt := Options{Width: width, Color: color}
				got := Render(md, opt)
				file := name + "." + map[bool]string{false: "plain", true: "color"}[color] + "." + map[int]string{30: "narrow", 80: "wide"}[width] + ".golden"
				path := filepath.Join("testdata", file)
				if *update {
					os.WriteFile(path, []byte(got), 0o644)
					continue
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run go test ./internal/termmd -update)", err)
				}
				if got != string(want) {
					t.Errorf("%s:\n got:\n%s\nwant:\n%s", file, got, want)
				}
				if !color && strings.Contains(got, "\x1b") {
					t.Errorf("%s: escape code with color off", file)
				}
				for _, l := range strings.Split(got, "\n") {
					if w := visibleWidth(l); w > width && name != "code" && name != "codeothers" {
						t.Errorf("%s: line %q is %d columns wide", file, l, w)
					}
				}
			}
		}
	}
}

// TestStreaming feeds every case in chunks of every size; the output must
// match rendering the whole text at once.
func TestStreaming(t *testing.T) {
	for name, md := range cases {
		want := Render(md, Options{Width: 40, Color: true})
		for size := 1; size <= 7; size++ {
			var b strings.Builder
			r := New(&b, Options{Width: 40, Color: true})
			for i := 0; i < len(md); i += size {
				r.Write(md[i:min(i+size, len(md))])
			}
			r.Flush()
			if b.String() != want {
				t.Errorf("%s chunk %d:\n%q\nwant\n%q", name, size, b.String(), want)
			}
		}
	}
}

// A fence split across chunks renders nothing until it closes.
func TestFenceSplitAcrossChunks(t *testing.T) {
	var b strings.Builder
	r := New(&b, Options{Width: 40})
	r.Write("Intro\n\n``")
	r.Write("`go\nx := 1\n")
	if strings.Contains(b.String(), "x := 1") {
		t.Fatalf("fence rendered before it closed: %q", b.String())
	}
	r.Write("``")
	r.Write("`\n")
	if !strings.Contains(b.String(), "| x := 1") {
		t.Fatalf("closed fence not rendered: %q", b.String())
	}
}

// A stream that ends mid-block still shows every word.
func TestFlushMidBlock(t *testing.T) {
	for _, md := range []string{"```go\nunclosed := true", "| a | b |\n|---|---|\n| 1 | 2", "- item without newline", "> quote and **bold", "partial line"} {
		var b strings.Builder
		r := New(&b, Options{Width: 40})
		r.Write(md)
		r.Flush()
		out := b.String()
		for _, word := range []string{"unclosed", "item", "quote", "partial", "1", "2"} {
			if strings.Contains(md, word) && !strings.Contains(out, word) {
				t.Errorf("%q lost %q: %q", md, word, out)
			}
		}
		if !strings.HasSuffix(out, "\n") {
			t.Errorf("%q: output does not end its line: %q", md, out)
		}
	}
}

func TestStringWidth(t *testing.T) {
	for s, w := range map[string]int{"abc": 3, "中文": 4, "日本語x": 7, "e\u0301": 1, "": 0} {
		if got := StringWidth(s); got != w {
			t.Errorf("StringWidth(%q) = %d, want %d", s, got, w)
		}
	}
}

func TestHyperlinks(t *testing.T) {
	out := Render("[docs](https://e.com)\n", Options{Width: 40, Color: true, Hyperlinks: true})
	if !strings.Contains(out, "\x1b]8;;https://e.com") {
		t.Errorf("no OSC 8: %q", out)
	}
	if out := Render("[docs](https://e.com)\n", Options{Width: 40, Color: true}); strings.Contains(out, "\x1b]8;") || !strings.Contains(out, "https://e.com") {
		t.Errorf("hyperlinks off: %q", out)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, true},
		{map[string]string{"WT_SESSION": "1"}, true},
		{map[string]string{"VTE_VERSION": "6003"}, true},
		{map[string]string{"VTE_VERSION": "4000"}, false},
		{map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX": "x"}, false},
		{map[string]string{"TERM": "dumb"}, false},
		{map[string]string{}, false},
	} {
		if got := SupportsHyperlinks(env(c.env)); got != c.want {
			t.Errorf("%v: %v", c.env, got)
		}
	}
}
