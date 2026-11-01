package tools

import (
	"fmt"
	"unicode/utf8"
)

// This file owns tool-output truncation. Both policies below share one
// UTF-8 boundary-safe core (cutAt), so neither can split a multibyte
// sequence, and both report the number of characters (runes) elided.
//
// Budgets are in bytes: max limits the bytes of s that are kept (truncate)
// or the bytes of the whole result (truncateRunes). Cut points move to the
// nearest rune boundary inside the budget, so the kept text never exceeds it.

// truncate keeps about max/2 bytes from each end of s and joins them with
// elidedMarker. The marker is not counted against max. For ASCII input the
// result is identical to the historical byte-slicing behavior. max <= 0
// means unlimited.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := max / 2
	return cutAt(s, half, len(s)-half)
}

// truncateRunes keeps the result, marker included, within max bytes. It
// returns "" when max cannot hold even the marker.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// The marker with the largest possible count bounds the real one: the
	// rune count elided never exceeds len(s).
	keep := max - len(elidedMarker(len(s)))
	if keep <= 0 {
		return ""
	}
	return cutAt(s, keep/2, len(s)-(keep-keep/2))
}

// cutAt returns s[:head] + marker + s[tail:], with head moved back and tail
// moved forward to UTF-8 rune boundaries so the result is valid UTF-8 when s
// is. The marker reports the runes removed.
func cutAt(s string, head, tail int) string {
	if head < 0 {
		head = 0
	}
	if tail > len(s) {
		tail = len(s)
	}
	if head > tail {
		head = tail
	}
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return s[:head] + elidedMarker(utf8.RuneCountInString(s[head:tail])) + s[tail:]
}

// elidedMarker is the notice placed where text was removed; n is in runes.
func elidedMarker(n int) string {
	return fmt.Sprintf("\n\n... [%d characters elided] ...\n\n", n)
}
