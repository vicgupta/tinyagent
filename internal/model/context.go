package model

import "strings"

// ContextWindows maps a model name prefix to its context window in tokens.
// The longest matching prefix wins. Unknown models return 0, meaning the
// window isn't known.
var ContextWindows = map[string]int{
	"gpt-4o":       128_000,
	"gpt-4o-mini":  128_000,
	"gpt-4.1":      1_047_576,
	"gpt-4.1-mini": 1_047_576,
	"gpt-4.1-nano": 1_047_576,
	"claude-":      200_000,
}

// ContextWindow returns the context window for spec, a "<provider>/<model>"
// name as given to New, or 0 when it isn't in ContextWindows. For a nested
// name such as openrouter/anthropic/claude-x, the last segment is tried too.
// A configured provider's model metadata, matched by exact name, comes
// before the table.
func ContextWindow(spec string) int {
	if m, ok := configuredMeta(spec); ok && m.ContextWindow > 0 {
		return m.ContextWindow
	}
	names := []string{spec}
	if _, name, ok := strings.Cut(spec, "/"); ok {
		names = append(names, name)
	}
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		names = append(names, spec[i+1:])
	}
	for _, name := range names {
		best, window := "", 0
		for prefix, v := range ContextWindows {
			if strings.HasPrefix(name, prefix) && len(prefix) > len(best) {
				best, window = prefix, v
			}
		}
		if window > 0 {
			return window
		}
	}
	return 0
}

// IsContextOverflow reports whether err is a provider's "request is larger
// than the context window" error.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, sub := range []string{"prompt is too long", "context_length_exceeded", "maximum context length"} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
