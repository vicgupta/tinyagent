package memory

import "strings"

// Search returns the active entries whose text or tags contain every word
// of query (case-insensitive) and that carry tag when tag is set, newest
// first, at most limit (0 means no limit). Pending entries never match: a
// person hasn't approved them.
func Search(entries []Entry, query, tag string, limit int) []Entry {
	words := strings.Fields(strings.ToLower(query))
	var want string
	if t := NormalizeTags([]string{tag}); len(t) == 1 {
		want = t[0]
	}
	var out []Entry
	for _, e := range Active(entries) {
		if want != "" && !hasTag(e, want) {
			continue
		}
		hay := strings.ToLower(e.Text + " " + strings.Join(e.Tags, " "))
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

// Recent returns the newest active entries, at most limit (0: all).
func Recent(entries []Entry, limit int) []Entry {
	a := Active(entries)
	if limit > 0 && len(a) > limit {
		a = a[:limit]
	}
	return a
}

func hasTag(e Entry, tag string) bool {
	for _, t := range e.Tags {
		if t == tag {
			return true
		}
	}
	return false
}
