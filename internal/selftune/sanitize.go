package selftune

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// reUnsafe matches text that tries to steer the agent rather than describe
// a failure: prompt-injection phrases, and anything about instructions
// files, config, state, permissions, sandboxing, hooks, credentials or
// destructive commands. Text from runs or the model that matches is never
// written into a skill.
var reUnsafe = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`ignore\s+(all\s+|any\s+|the\s+)?(previous|prior|above|earlier)`,
	`disregard`, `forget\s+(all|your|previous)`, `system\s+prompt`, `you\s+are\s+now`,
	`agents\.md`, `claude\.md`, `tinyagent\.ya?ml`, `memory\.toml`, `\.tinyagent`, `\.env\b`,
	`permission`, `sandbox`, `lockdown`, `\bhooks?\b`, `trust-config`, `--yolo`, `dangerous`,
	`allow[\s_-]*all`, `approve`, `\bsudo\b`, `rm\s+-rf`, `\bcurl\b`, `\bwget\b`,
	`api[\s_-]*key`, `password`, `secret`, `credential`, `\btoken\b`,
	`write\s+(to|into)\s+`, `edit\s+(the\s+)?(file|config)`,
}, "|"))

// Unsafe reports whether s looks like instructions or touches safety
// policy; see reUnsafe.
func Unsafe(s string) bool { return reUnsafe.MatchString(s) }

// clean redacts s, flattens it to one line of printable text without
// Markdown code or heading markup, and caps it at n bytes.
func clean(s string, n int, redact func(string) string) string {
	if redact != nil {
		s = redact(s)
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '`':
			return '\''
		case unicode.IsControl(r) || unicode.IsSpace(r):
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, "#>-*+ ")
	return capBytes(s, n)
}

// capBytes cuts s to at most n bytes on a rune boundary, adding "...".
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// excerpt redacts and caps a multi-line excerpt.
func excerpt(s string, n int, redact func(string) string) string {
	if redact != nil {
		s = redact(s)
	}
	if len(s) > n {
		return capBytes(s, n) + "\n[truncated]"
	}
	return s
}

// safeLesson validates instruction text from the model: one line, within
// MaxLesson, no code fences or headings, nothing unsafe. It returns the
// cleaned text and "" or why it was refused.
func safeLesson(s string) (string, string) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", "empty reply"
	case strings.Contains(s, "```") || strings.Contains(s, "~~~"):
		return "", "reply has a code fence"
	case strings.ContainsAny(s, "\r\n"):
		return "", "reply is more than one line"
	case len(s) > MaxLesson:
		return "", "reply is longer than the lesson limit"
	case Unsafe(s):
		return "", "reply looks like instructions about files, policy or credentials"
	}
	out := strings.TrimLeft(s, "#>-*+ ")
	if out == "" {
		return "", "empty reply"
	}
	return out, ""
}
