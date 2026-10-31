package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateMultibyteValidUTF8(t *testing.T) {
	in := "你好世界你好"
	for max := 1; max <= len(in)+1; max++ {
		got := Truncate(in, max)
		if !utf8.ValidString(got) {
			t.Fatalf("Truncate(%q, %d) = %q is not valid UTF-8", in, max, got)
		}
	}
	// max 5 keeps 2 bytes at each end, which is less than one 3-byte rune,
	// so both cuts move to a boundary and all 6 runes are elided.
	if got, want := Truncate(in, 5), elidedMarker(6); got != want {
		t.Errorf("Truncate(%q, 5) = %q, want %q", in, got, want)
	}
	// max 6 keeps exactly one rune at each end.
	if got, want := Truncate(in, 6), "你"+elidedMarker(4)+"好"; got != want {
		t.Errorf("Truncate(%q, 6) = %q, want %q", in, got, want)
	}
}

func TestTruncateRuneCountAccurate(t *testing.T) {
	in := strings.Repeat("é", 50) // 100 bytes, 50 runes
	for _, max := range []int{3, 7, 10, 33, 99} {
		got := Truncate(in, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max %d: invalid UTF-8", max)
		}
		i := strings.Index(got, elidedMarker(0)[:7])
		if i < 0 {
			t.Fatalf("max %d: no marker in %q", max, got)
		}
		kept := strings.Count(got[:i], "é") + strings.Count(got[i:], "é")
		want := elidedMarker(50 - kept)
		if !strings.Contains(got, want) {
			t.Errorf("max %d: got %q, want marker %q", max, got, want)
		}
		if head, tail := len(got[:i]), len(got)-strings.LastIndex(got, "\n\n")-2; head+tail > max {
			t.Errorf("max %d: kept %d bytes", max, head+tail)
		}
	}
}

func TestTruncateASCIIUnchanged(t *testing.T) {
	in := strings.Repeat("a", 10) + strings.Repeat("b", 10)
	want := strings.Repeat("a", 4) + "\n\n... [12 characters elided] ...\n\n" + strings.Repeat("b", 4)
	if got := Truncate(in, 8); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Truncate(in, 0); got != in {
		t.Errorf("max 0 should be unlimited")
	}
	if got := Truncate(in, 20); got != in {
		t.Errorf("fitting input should be unchanged")
	}
}

func TestTruncateRunesBudgetAndUTF8(t *testing.T) {
	in := strings.Repeat("你好世界", 40)
	for max := 0; max <= 120; max++ {
		got := truncateRunes(in, max)
		if len(got) > max {
			t.Fatalf("max %d: len %d", max, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("max %d: invalid UTF-8 %q", max, got)
		}
		if got != "" {
			i := strings.Index(got, "\n\n... [")
			j := strings.LastIndex(got, " ...\n\n")
			kept := utf8.RuneCountInString(got[:i]) + utf8.RuneCountInString(got[j+6:])
			if want := elidedMarker(utf8.RuneCountInString(in) - kept); !strings.Contains(got, want) {
				t.Fatalf("max %d: got %q, want marker %q", max, got, want)
			}
		}
	}
}
