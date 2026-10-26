package main

import (
	"cmp"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
)

// unknownFlagPrefix starts the error the flag package returns for an
// undefined flag; the flag name (without dashes) follows it.
const unknownFlagPrefix = "flag provided but not defined: -"

// reportUnknownFlag prints a one-line error for an undefined flag, echoed as
// the user typed it in args, with up to three close flag names. It reports
// false if err isn't an unknown-flag error.
func reportUnknownFlag(w io.Writer, fs *flag.FlagSet, args []string, err error) bool {
	name, ok := strings.CutPrefix(err.Error(), unknownFlagPrefix)
	if !ok {
		return false
	}
	typed := "--" + name
	for _, a := range args {
		if a == "--" {
			break
		}
		dashes := len(a) - len(strings.TrimLeft(a, "-"))
		if n, _, _ := strings.Cut(a[dashes:], "="); n == name && (dashes == 1 || dashes == 2) {
			typed = a[:dashes] + name
			break
		}
	}
	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	msg := "error: unknown flag " + typed
	if s := suggestFlags(name, names); len(s) > 0 {
		msg += "; did you mean --" + strings.Join(s, ", --") + "?"
	}
	fmt.Fprintln(w, msg+" (see tinyagent --help)")
	return true
}

// suggestFlags returns up to three of names close to the mistyped name: within
// a small edit distance (2, or 1 for names under 5 characters) or extended by
// it (name is a prefix of at least 3 characters), closest first. Single-letter
// names are never suggested or matched: every letter is one edit from another.
func suggestFlags(name string, names []string) []string {
	if len(name) < 2 {
		return nil
	}
	limit := 2
	if len(name) < 5 {
		limit = 1
	}
	type match struct {
		name  string
		score int
	}
	var ms []match
	for _, n := range names {
		if len(n) < 2 {
			continue
		}
		if d := levenshtein(name, n); d <= limit {
			ms = append(ms, match{n, d})
		} else if len(name) >= 3 && strings.HasPrefix(n, name) {
			ms = append(ms, match{n, limit + 1})
		}
	}
	slices.SortFunc(ms, func(a, b match) int {
		return cmp.Or(cmp.Compare(a.score, b.score), cmp.Compare(a.name, b.name))
	})
	var out []string
	for _, m := range ms[:min(len(ms), 3)] {
		out = append(out, m.name)
	}
	return out
}

// levenshtein returns the edit distance between a and b in bytes.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
