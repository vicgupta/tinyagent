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
	var b strings.Builder
	b.WriteString(renderHeader)
	var r Rendered
	for i, e := range active {
		line := fmt.Sprintf("- [%s, %s] %s\n", e.ID, e.UpdatedAt.UTC().Format("2006-01-02"), e.Text)
		if b.Len()+len(line) > maxBytes {
			r.Omitted = len(active) - i
			break
		}
		b.WriteString(line)
		r.Loaded = append(r.Loaded, e)
	}
	if r.Omitted > 0 {
		fmt.Fprintf(&b, "\n(%d older memory %s omitted to stay within the size limit.)\n", r.Omitted, plural(r.Omitted, "entry", "entries"))
	}
	r.Section = b.String()
	return r
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
