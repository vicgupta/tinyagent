package eventlog

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Redacted replaces every secret the Redactor finds.
const Redacted = "[REDACTED]"

// minSecretLen keeps short values (e.g. "1" or "true") from being treated as
// secrets and redacting unrelated text.
const minSecretLen = 6

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9\-._~+/]+=*`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`),
}

// Redactor masks secret values and token-shaped strings.
type Redactor struct {
	r *strings.Replacer
}

// SecretEnvName reports whether an environment variable's value is treated
// as a secret: names ending in _KEY, _TOKEN, _SECRET or _PASSWORD.
func SecretEnvName(name string) bool {
	n := strings.ToUpper(name)
	for _, suf := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD"} {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return false
}

// NewRedactor masks the values of secret-named variables in env (KEY=VALUE
// entries, typically os.Environ()) and the given extra values, such as env
// values set in the config.
func NewRedactor(env []string, extra ...string) *Redactor {
	seen := map[string]bool{}
	var vals []string
	add := func(v string) {
		if len(v) < minSecretLen || seen[v] {
			return
		}
		seen[v] = true
		vals = append(vals, v)
		// The same value as it appears inside a JSON string.
		if b, err := json.Marshal(v); err == nil {
			if q := string(b[1 : len(b)-1]); q != v && !seen[q] {
				seen[q] = true
				vals = append(vals, q)
			}
		}
	}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && SecretEnvName(k) {
			add(v)
		}
	}
	for _, v := range extra {
		add(v)
	}
	// Longest first, so a secret containing another is masked whole.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	pairs := make([]string, 0, 2*len(vals))
	for _, v := range vals {
		pairs = append(pairs, v, Redacted)
	}
	return &Redactor{r: strings.NewReplacer(pairs...)}
}

// EnvRedactor is NewRedactor(os.Environ(), extra...).
func EnvRedactor(extra ...string) *Redactor { return NewRedactor(os.Environ(), extra...) }

// Redact returns s with secrets masked. A nil Redactor still masks
// token-shaped strings.
func (r *Redactor) Redact(s string) string {
	if r != nil && r.r != nil {
		s = r.r.Replace(s)
	}
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, Redacted)
	}
	return s
}
