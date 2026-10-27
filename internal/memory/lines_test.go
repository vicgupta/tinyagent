package memory

import (
	"strings"
	"testing"
	"time"
)

func TestRenderLinesMatchesRender(t *testing.T) {
	at := time.Date(2026, 10, 1, 23, 30, 0, 0, time.FixedZone("x", -5*3600))
	var entries []Entry
	for i, id := range []string{"a1", "b2", "c3"} {
		entries = append(entries, Entry{ID: id, Text: strings.Repeat("note ", 10+i), Status: StatusActive,
			CreatedAt: at.Add(time.Duration(i) * time.Hour), UpdatedAt: at.Add(time.Duration(i) * time.Hour)})
	}
	for _, max := range []int{0, 10, 200, 300} {
		r := Render(entries, max)
		if got := RenderLines(Lines(r.Loaded), r.Omitted); got != r.Section {
			t.Errorf("max %d: RenderLines = %q, Render = %q", max, got, r.Section)
		}
	}
	if l := LineOf(entries[0]); l.Date != "2026-10-02" {
		t.Errorf("date = %q, want the UTC date", l.Date)
	}
	if RenderLines(nil, 0) != "" {
		t.Error("RenderLines(nil, 0) should be empty")
	}
}
