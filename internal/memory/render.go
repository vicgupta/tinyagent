package memory

import (
	"fmt"
	"strings"
)

const renderHeader = `# Project memory

The notes below were saved by earlier tinyagent runs on this project. They may be outdated or wrong: check them against the code before relying on them. AGENTS.md and the task outrank them.

`

// Rendered is the prompt section for one run plus what it contains.
type Rendered struct {
	// Section is the text to add to the system prompt, or "" when no entry
	// is active.
	Section string
	// Loaded are the entries in Section, newest first.
	Loaded []Entry
	// Omitted counts active entries left out to stay within maxBytes.
	Omitted int
}

// Render builds the "Project memory" prompt section from the active
// entries, newest first, keeping the newest entries whose lines fit within
// maxBytes (<= 0 means DefaultMaxBytes). Pending entries are never rendered.
// When entries are omitted, the section says how many.
func Render(entries []Entry, maxBytes int) Rendered {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	active := Active(entries)
	if len(active) == 0 {
		return Rendered{}
	}
	var r Rendered
	size := len(renderHeader)
	for i, e := range active {
		n := len(renderLine(LineOf(e)))
		if size+n > maxBytes {
			r.Omitted = len(active) - i
			break
		}
		size += n
		r.Loaded = append(r.Loaded, e)
	}
	r.Section = RenderLines(Lines(r.Loaded), r.Omitted)
	return r
}

// Line is one loaded entry as it appears in the prompt section: its id,
// its date (YYYY-MM-DD, UTC) and its text. A run records its loaded lines
// so a resume can rebuild the identical section without reading the store.
type Line struct {
	ID   string `json:"id"`
	Date string `json:"date"`
	Text string `json:"text"`
}

// LineOf returns e as a prompt line.
func LineOf(e Entry) Line {
	return Line{ID: e.ID, Date: e.UpdatedAt.UTC().Format("2006-01-02"), Text: e.Text}
}

// Lines returns entries as prompt lines, in order.
func Lines(entries []Entry) []Line {
	if len(entries) == 0 {
		return nil
	}
	out := make([]Line, 0, len(entries))
	for _, e := range entries {
		out = append(out, LineOf(e))
	}
	return out
}

func renderLine(l Line) string {
	return fmt.Sprintf("- [%s, %s] %s\n", l.ID, l.Date, l.Text)
}

// RenderLines builds the prompt section from already-selected lines and the
// number of omitted entries, exactly as Render does. It returns "" when
// there are no lines and nothing was omitted.
func RenderLines(lines []Line, omitted int) string {
	if len(lines) == 0 && omitted == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(renderHeader)
	for _, l := range lines {
		b.WriteString(renderLine(l))
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n(%d older memory %s omitted to stay within the size limit.)\n", omitted, plural(omitted, "entry", "entries"))
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
