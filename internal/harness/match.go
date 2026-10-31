package harness

import "sort"

// MatchWords scores task against one candidate the way Route does: the
// number of distinct content words task shares with intent, plus 100 when
// task names the candidate. It also returns the shared words (stemmed,
// sorted) and the name when it matched, so callers can say why.
func MatchWords(task, name, intent string) (int, []string) {
	words := contentWords(task)
	var shared []string
	for w := range contentWords(intent) {
		if words[w] {
			shared = append(shared, w)
		}
	}
	sort.Strings(shared)
	n := score(words, task, name, intent)
	if n >= 100 {
		shared = append([]string{name}, shared...)
	}
	return n, shared
}
