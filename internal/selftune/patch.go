package selftune

import (
	"fmt"
	"regexp"
	"strings"
)

// appendLesson adds bullet to the skill's lessons section: under an
// existing trailing "## Lessons from past runs" heading, else in a new
// one at the end of the file.
func appendLesson(content, bullet string) string {
	nl := "\n"
	if strings.Contains(content, "\r\n") {
		nl = "\r\n"
	}
	body := strings.TrimRight(content, "\r\n")
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	last := -1
	fence := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if !fence && strings.HasPrefix(t, "## ") {
			last = i
		}
	}
	if !fence && last >= 0 && strings.TrimSpace(lines[last]) == LessonsHeading {
		return body + nl + "- " + bullet + nl
	}
	return body + nl + nl + LessonsHeading + nl + nl + "- " + bullet + nl
}

var reCheckLine = regexp.MustCompile(`(?i)^\s*\**\s*success check\s*:?`)

// strengthenCheck appends clause to the skill's success check line (the
// first one outside a code fence). ok is false when there is none.
func strengthenCheck(content, clause string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	fence := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if fence || !reCheckLine.MatchString(t) {
			continue
		}
		end := ""
		body := l
		if strings.HasSuffix(body, "\r\n") {
			body, end = strings.TrimSuffix(body, "\r\n"), "\r\n"
		} else if strings.HasSuffix(body, "\n") {
			body, end = strings.TrimSuffix(body, "\n"), "\n"
		}
		lines[i] = strings.TrimRight(body, " \t") + " " + clause + end
		return strings.Join(lines, ""), true
	}
	return content, false
}

// unifiedDiff is a minimal unified diff of a and b with 3 lines of
// context around the one changed region (candidates change one place).
func unifiedDiff(path, a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	p := 0
	for p < len(al) && p < len(bl) && al[p] == bl[p] {
		p++
	}
	s := 0
	for s < len(al)-p && s < len(bl)-p && al[len(al)-1-s] == bl[len(bl)-1-s] {
		s++
	}
	const ctx = 3
	start := max(p-ctx, 0)
	aEnd, bEnd := len(al)-s, len(bl)-s
	tail := min(s, ctx)
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	an := aEnd - start + tail
	bn := bEnd - start + tail
	fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", start+1, an, start+1, bn)
	for _, l := range al[start:p] {
		out.WriteString(" " + l + "\n")
	}
	for _, l := range al[p:aEnd] {
		out.WriteString("-" + l + "\n")
	}
	for _, l := range bl[p:bEnd] {
		out.WriteString("+" + l + "\n")
	}
	for _, l := range al[aEnd : aEnd+tail] {
		out.WriteString(" " + l + "\n")
	}
	return out.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
