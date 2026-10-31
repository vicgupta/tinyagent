package triage

import (
	"regexp"
	"strings"
)

// contentWords and stem are copied from internal/harness/router.go so the
// two packages stay independent.

var reWord = regexp.MustCompile(`[a-z0-9]+`)

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true,
	"into": true, "before": true, "after": true, "any": true, "all": true, "each": true, "are": true,
	"was": true, "you": true, "your": true, "our": true, "its": true, "it": true, "then": true,
	"them": true, "they": true, "not": true, "but": true, "can": true, "will": true, "should": true,
	"must": true, "make": true, "small": true, "only": true, "one": true, "use": true, "using": true,
}

// contentWords lowercases s, drops short and stop words and strips simple
// English suffixes so "tests" matches "test".
func contentWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range reWord.FindAllString(strings.ToLower(s), -1) {
		if len(w) < 3 || stopWords[w] {
			continue
		}
		out[stem(w)] = true
	}
	return out
}

func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			w = strings.TrimSuffix(w, suf)
			break
		}
	}
	if n := len(w); n >= 4 && w[n-1] == w[n-2] && !strings.ContainsRune("aeiou", rune(w[n-1])) {
		w = w[:n-1]
	}
	return w
}
