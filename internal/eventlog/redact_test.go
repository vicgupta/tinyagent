package eventlog

import (
	"strings"
	"testing"
)

func TestValueRedactorLongestFirstAndEscaped(t *testing.T) {
	r := NewValueRedactor([]string{"abcdef", "abcdefgh", `quo"te12`, "short"}, false)
	if r.Len() != 3 {
		t.Errorf("Len = %d, want 3 (short value skipped)", r.Len())
	}
	got, n := r.RedactCount(`x abcdefgh y abcdef {"v":"quo\"te12"} short`)
	want := `x [REDACTED] y [REDACTED] {"v":"[REDACTED]"} short`
	if got != want || n != 3 {
		t.Errorf("RedactCount = %q, %d; want %q, 3", got, n, want)
	}
}

func TestValueRedactorPatternsOptIn(t *testing.T) {
	tok := "ghp_" + strings.Repeat("a", 30)
	if got := NewValueRedactor(nil, false).Redact(tok); got != tok {
		t.Errorf("patterns off: %q", got)
	}
	if got := NewValueRedactor(nil, true).Redact(tok); got != Redacted {
		t.Errorf("patterns on: %q", got)
	}
	// The event log's redactor keeps masking shapes, also when nil.
	var nilR *Redactor
	if got := nilR.Redact(tok); got != Redacted {
		t.Errorf("nil Redact: %q", got)
	}
	if got, n := nilR.RedactCount(tok); got != tok || n != 0 {
		t.Errorf("nil RedactCount: %q, %d", got, n)
	}
}

func TestValueRedactorAddAndPartialSuffix(t *testing.T) {
	r := NewValueRedactor(nil, false)
	if got := r.Redact("tok-123456"); got != "tok-123456" {
		t.Fatalf("empty redactor changed text: %q", got)
	}
	r.Add("tok-123456")
	if got := r.Redact("a tok-123456 b"); got != "a [REDACTED] b" {
		t.Errorf("after Add: %q", got)
	}
	for s, want := range map[string]int{
		"out: tok-12":     6,
		"out: tok-123456": 0, // a whole value is not held back
		"out: t":          1,
		"out: x":          0,
		"":                0,
	} {
		if got := r.PartialSuffix(s); got != want {
			t.Errorf("PartialSuffix(%q) = %d, want %d", s, got, want)
		}
	}
}
