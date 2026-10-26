package eventlog

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redacted replaces every secret the Redactor finds.
const Redacted = "[REDACTED]"

// MinSecretLen keeps short values (e.g. "1" or "true") from being treated
// as secrets and redacting unrelated text.
const MinSecretLen = 6

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9\-._~+/]+=*`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`),
}

// Redactor masks secret values and, optionally, token-shaped strings. It
// is safe for concurrent use.
type Redactor struct {
	mu       sync.RWMutex
	r        *strings.Replacer
	vals     []string // longest first, JSON-escaped forms included
	seen     map[string]bool
	n        int // distinct values added, not counting escaped forms
	patterns bool
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
// values set in the config, plus token-shaped strings.
func NewRedactor(env []string, extra ...string) *Redactor {
	var vals []string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && SecretEnvName(k) {
			vals = append(vals, v)
		}
	}
	return NewValueRedactor(append(vals, extra...), true)
}

// NewValueRedactor masks exactly vals (values shorter than MinSecretLen
// are skipped), each also in its JSON-escaped form, and with patterns also
// token-shaped strings.
func NewValueRedactor(vals []string, patterns bool) *Redactor {
	r := &Redactor{seen: map[string]bool{}, patterns: patterns}
	r.Add(vals...)
	return r
}

// EnvRedactor is NewRedactor(os.Environ(), extra...).
func EnvRedactor(extra ...string) *Redactor { return NewRedactor(os.Environ(), extra...) }

// Add registers more secret values, such as credentials refreshed during
// the run.
func (r *Redactor) Add(vals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	changed := false
	add := func(v string) {
		if len(v) < MinSecretLen || r.seen[v] {
			return
		}
		r.seen[v] = true
		r.vals = append(r.vals, v)
		changed = true
	}
	for _, v := range vals {
		if len(v) < MinSecretLen || r.seen[v] {
			continue
		}
		r.n++
		add(v)
		// The same value as it appears inside a JSON string.
		if b, err := json.Marshal(v); err == nil {
			if q := string(b[1 : len(b)-1]); q != v {
				add(q)
			}
		}
	}
	if !changed && r.r != nil {
		return
	}
	// Longest first, so a secret containing another is masked whole.
	sort.SliceStable(r.vals, func(i, j int) bool { return len(r.vals[i]) > len(r.vals[j]) })
	pairs := make([]string, 0, 2*len(r.vals))
	for _, v := range r.vals {
		pairs = append(pairs, v, Redacted)
	}
	r.r = strings.NewReplacer(pairs...)
}

// Len is the number of distinct secret values r masks.
func (r *Redactor) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.n
}

// Redact returns s with secrets masked. A nil Redactor still masks
// token-shaped strings.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return redactPatterns(s)
	}
	s, _ = r.RedactCount(s)
	return s
}

// RedactCount is Redact plus how many secrets it masked.
func (r *Redactor) RedactCount(s string) (string, int) {
	if r == nil {
		return s, 0
	}
	before := strings.Count(s, Redacted)
	r.mu.RLock()
	if r.r != nil && len(r.vals) > 0 {
		s = r.r.Replace(s)
	}
	patterns := r.patterns
	r.mu.RUnlock()
	if patterns {
		s = redactPatterns(s)
	}
	return s, max(strings.Count(s, Redacted)-before, 0)
}

func redactPatterns(s string) string {
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, Redacted)
	}
	return s
}

// PartialSuffix returns the length of the longest suffix of s that is the
// start, but not the whole, of a secret value: output a reader should hold
// back until more arrives, so a secret split across reads is still masked.
func (r *Redactor) PartialSuffix(s string) int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := 0
	for _, v := range r.vals {
		for k := min(len(v)-1, len(s)); k > best; k-- {
			if strings.HasSuffix(s, v[:k]) {
				best = k
				break
			}
		}
	}
	return best
}
