package playbooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/harness"
)

// Routing methods.
const (
	MethodNamed    = "named"
	MethodKeywords = "keywords"
	MethodModel    = "model"
)

// Picker asks a model to pick a playbook: given the prompt, it returns a
// playbook name or "none". Injectable for tests.
type Picker func(ctx context.Context, prompt string) (string, error)

// Selection is the outcome of routing a task.
type Selection struct {
	Playbook *Playbook
	Method   string
	Keywords []string
	// Note says why no playbook was chosen (empty when one was).
	Note string
}

// Line is the stderr line for s.
func (s Selection) Line() string {
	if s.Playbook == nil {
		return "playbook: none (" + s.Note + ")"
	}
	switch s.Method {
	case MethodKeywords:
		q := make([]string, len(s.Keywords))
		for i, k := range s.Keywords {
			q[i] = fmt.Sprintf("%q", k)
		}
		return fmt.Sprintf("playbook: %s (keywords: %s)", s.Playbook.Name, strings.Join(q, ", "))
	case MethodModel:
		return "playbook: " + s.Playbook.Name + " (model fallback)"
	}
	return "playbook: " + s.Playbook.Name + " (--playbook)"
}

// matchText is what keyword scoring reads: intent plus match phrases.
func matchText(p *Playbook) string { return p.Intent + " " + strings.Join(p.Match, " ") }

// Keywords scores task against every playbook with the harness router's
// scoring. It returns the clear best match (nil on a tie or a miss) and
// the words that matched.
func Keywords(pbs []*Playbook, task string) (*Playbook, []string) {
	var best *Playbook
	var bestWords []string
	bestScore, tie := 0, false
	for _, p := range pbs {
		n, words := harness.MatchWords(task, p.Name, matchText(p))
		switch {
		case n > bestScore:
			best, bestWords, bestScore, tie = p, words, n, false
		case n == bestScore && n > 0:
			tie = true
		}
	}
	if best == nil || tie || bestScore < harness.MinRouteScore {
		return nil, nil
	}
	return best, bestWords
}

// Select routes task: a clear keyword match wins; on a tie or miss pick
// (nil when the fallback is off) is asked once. A failed call or "none"
// means no playbook, with Note saying so.
func Select(ctx context.Context, pbs []*Playbook, task string, pick Picker) Selection {
	if len(pbs) == 0 {
		return Selection{Note: "no playbooks in ./playbooks"}
	}
	if p, words := Keywords(pbs, task); p != nil {
		return Selection{Playbook: p, Method: MethodKeywords, Keywords: words}
	}
	if pick == nil {
		return Selection{Note: "no clear keyword match; model fallback is off"}
	}
	ans, err := pick(ctx, PickPrompt(pbs, task))
	if err != nil {
		return Selection{Note: "no clear keyword match; model fallback failed: " + err.Error()}
	}
	name := strings.ToLower(strings.Trim(strings.TrimSpace(ans), "\"'`.*"))
	if f := strings.Fields(name); len(f) > 0 {
		name = f[0]
	}
	if name == "" || name == "none" {
		return Selection{Note: "no clear keyword match; model fallback chose none"}
	}
	if p, ok := Get(pbs, name); ok {
		return Selection{Playbook: p, Method: MethodModel}
	}
	return Selection{Note: fmt.Sprintf("no clear keyword match; model fallback answered %q, not a playbook", ans)}
}

// PickPrompt is the single prompt the model fallback gets.
func PickPrompt(pbs []*Playbook, task string) string {
	var b strings.Builder
	b.WriteString("Pick the playbook that fits this task best, or none if none clearly fits.\nAnswer with the playbook name only, or none.\n\nPlaybooks:\n")
	for _, p := range pbs {
		intent := p.Intent
		if len(intent) > 300 {
			intent = intent[:300] + "…"
		}
		fmt.Fprintf(&b, "- %s: %s\n", p.Name, strings.ReplaceAll(intent, "\n", " "))
	}
	fmt.Fprintf(&b, "\nTask:\n%s\n", task)
	return b.String()
}
