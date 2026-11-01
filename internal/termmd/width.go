package termmd

import "unicode"

// RuneWidth is the number of terminal columns r takes: 0 for controls and
// combining marks, 2 for East Asian wide and fullwidth characters and most
// emoji, 1 otherwise. It is a small table, not a full UAX #11
// implementation, but covers CJK, Hangul, kana, fullwidth forms and emoji.
func RuneWidth(r rune) int {
	switch {
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		return 0
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff:
		return 0
	case r >= 0xfe00 && r <= 0xfe0f: // variation selectors
		return 0
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		return 0
	}
	if wide(r) {
		return 2
	}
	return 1
}

func wide(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f || // Hangul Jamo
		r == 0x2329 || r == 0x232a ||
		(r >= 0x231a && r <= 0x231b) ||
		(r >= 0x23e9 && r <= 0x23ec) ||
		(r >= 0x25fd && r <= 0x25fe) ||
		(r >= 0x2614 && r <= 0x2615) ||
		(r >= 0x2648 && r <= 0x2653) ||
		(r >= 0x26aa && r <= 0x26ab) ||
		(r >= 0x26bd && r <= 0x26be) ||
		(r >= 0x26c4 && r <= 0x26c5) ||
		r == 0x2705 || r == 0x270a || r == 0x270b || r == 0x2728 || r == 0x274c ||
		(r >= 0x2753 && r <= 0x2755) || r == 0x2757 ||
		(r >= 0x2795 && r <= 0x2797) || r == 0x27b0 || r == 0x27bf ||
		(r >= 0x2b1b && r <= 0x2b1c) || r == 0x2b50 || r == 0x2b55 ||
		(r >= 0x2e80 && r <= 0x303e) ||
		(r >= 0x3041 && r <= 0x33ff) ||
		(r >= 0x3400 && r <= 0x4dbf) ||
		(r >= 0x4e00 && r <= 0x9fff) ||
		(r >= 0xa000 && r <= 0xa4cf) ||
		(r >= 0xa960 && r <= 0xa97f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f680 && r <= 0x1f6ff) ||
		(r >= 0x1f900 && r <= 0x1f9ff) ||
		(r >= 0x1fa70 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd))
}

// StringWidth is the number of terminal columns s takes. s must not hold
// escape sequences.
func StringWidth(s string) int {
	n := 0
	for _, r := range s {
		n += RuneWidth(r)
	}
	return n
}
