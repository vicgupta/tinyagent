package harness

import (
	"strings"
	"testing"
)

func TestMatchWords(t *testing.T) {
	n, words := MatchWords("fix the failing test", "bug-fix", "reproduce a bug, fix it and add a failing test")
	if n != 3 || strings.Join(words, ",") != "fail,fix,test" {
		t.Errorf("n %d, words %q", n, words)
	}
	n, words = MatchWords("run bug-fix now", "bug-fix", "repair")
	if n != 100 || len(words) != 1 || words[0] != "bug-fix" {
		t.Errorf("named: n %d, words %q", n, words)
	}
}
