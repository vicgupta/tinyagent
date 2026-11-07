package main

import (
	"fmt"
	"strings"
)

// modelStatusText is /model with no argument: the current main model, its
// fallback chain, and any role routes that differ from main (the same set
// --check reports). st styles labels; with styling off the text is plain.
func modelStatusText(s *session, st styler) string {
	var b strings.Builder
	row := func(k, v string) {
		fmt.Fprintf(&b, "  %s %s\n", st.Dim(fmt.Sprintf("%-11s", k)), v)
	}
	row("model", s.modelName)
	switch {
	case len(s.fallbacks) == 0:
		row("fallback", "none")
	default:
		row("fallback", strings.Join(s.fallbacks, ", "))
	}
	for _, r := range checkRoutes(s.cfg, s.modelName) {
		line := r.Model + " (" + r.Role + ", from " + r.Source + ")"
		if len(r.Fallbacks) > 0 {
			line += ", fallback " + strings.Join(r.Fallbacks, " -> ")
		}
		row(r.Task, line)
	}
	return b.String()
}
