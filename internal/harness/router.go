package harness

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MinRouteScore is how many distinct content words a task must share with
// an intent for the router to pick it.
const MinRouteScore = 2

// Route is the router's decision: the pipeline to run and why.
type Route struct {
	Pipeline *Pipeline `json:"-"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"` // "workflow" or "skill"
	// How is "named" (--workflow-name), "intent" (matched the task) or
	// "default" (the file's default).
	How   string `json:"how"`
	Score int    `json:"score,omitempty"`
}

// RouteError is returned when the router can't choose; no stage has run.
type RouteError struct{ Msg string }

func (e *RouteError) Error() string { return e.Msg }

// Route picks what to run for task. An explicit name (a workflow or a skill)
// wins; otherwise the task is matched against every workflow's intent and
// every skill's intent line, and the single best match with at least
// MinRouteScore shared words is chosen. With no match (or a tie) the file's
// default is used if it has one; otherwise Route returns a RouteError rather
// than guessing.
func (w *Workflow) Route(task, name string) (Route, error) {
	if name != "" {
		r, ok := w.byName(name)
		if !ok {
			return Route{}, &RouteError{fmt.Sprintf("no workflow or skill named %q in %s (have: %s)", name, w.Path, strings.Join(w.names(), ", "))}
		}
		r.How = "named"
		return r, nil
	}
	type cand struct {
		name, kind string
		score      int
	}
	words := contentWords(task)
	var cs []cand
	for _, n := range sortedNames(w.Pipelines) {
		p := w.Pipelines[n]
		intent := p.Intent
		if intent == "" {
			var parts []string
			for _, st := range p.Stages {
				parts = append(parts, st.Skill.Intent)
			}
			intent = strings.Join(parts, " ")
		}
		cs = append(cs, cand{n, "workflow", score(words, task, n, intent)})
	}
	for _, n := range sortedNames(w.Skills) {
		if w.Skills[n].SuccessCheck == "" {
			continue // needs a stage check command, so only usable in a workflow
		}
		cs = append(cs, cand{n, "skill", score(words, task, n, w.Skills[n].Intent)})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].score > cs[j].score })
	var why string
	switch {
	case len(cs) == 0 || cs[0].score < MinRouteScore:
		why = "no workflow or skill intent matches the task"
	case len(cs) > 1 && cs[1].score == cs[0].score:
		why = fmt.Sprintf("the task matches %s and %s equally", cs[0].name, cs[1].name)
	default:
		r, _ := w.byName(cs[0].name)
		r.How, r.Score = "intent", cs[0].score
		return r, nil
	}
	if w.Default != "" {
		r, _ := w.byName(w.Default)
		r.How = "default"
		return r, nil
	}
	return Route{}, &RouteError{fmt.Sprintf("%s; pass --workflow-name (have: %s) or set default: in %s", why, strings.Join(w.names(), ", "), w.Path)}
}

func (w *Workflow) byName(name string) (Route, bool) {
	if p, ok := w.Pipelines[name]; ok {
		return Route{Pipeline: p, Name: name, Kind: "workflow"}, true
	}
	if sk, ok := w.Skills[name]; ok && sk.SuccessCheck != "" {
		return Route{Pipeline: &Pipeline{Name: name, Intent: sk.Intent, Stages: []Stage{w.single(sk)}}, Name: name, Kind: "skill"}, true
	}
	return Route{}, false
}

func (w *Workflow) names() []string {
	out := sortedNames(w.Pipelines)
	for _, n := range sortedNames(w.Skills) {
		if w.Skills[n].SuccessCheck != "" {
			out = append(out, n)
		}
	}
	return out
}

// score counts the distinct content words task shares with intent; naming
// the candidate in the task counts as a strong match.
func score(words map[string]bool, task, name, intent string) int {
	n := 0
	for w := range contentWords(intent) {
		if words[w] {
			n++
		}
	}
	if regexp.MustCompile(`(?i)(^|[^a-z0-9_-])` + regexp.QuoteMeta(name) + `($|[^a-z0-9_-])`).MatchString(task) {
		n += 100
	}
	return n
}

var reWord = regexp.MustCompile(`[a-z0-9]+`)

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true,
	"into": true, "before": true, "after": true, "any": true, "all": true, "each": true, "are": true,
	"was": true, "you": true, "your": true, "our": true, "its": true, "it": true, "then": true,
	"them": true, "they": true, "not": true, "but": true, "can": true, "will": true, "should": true,
	"must": true, "make": true, "small": true, "only": true, "one": true, "use": true, "using": true,
}

// contentWords lowercases s, drops short and stop words and strips simple
// English suffixes so "tests" matches "test" and "planning" matches "plan".
func contentWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range reWord.FindAllString(strings.ToLower(s), -1) {
		if len(w) < 3 || stopWords[w] {
			continue
		}
		out[stem(w)] = true
	}
	return out
}

func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			w = strings.TrimSuffix(w, suf)
			break
		}
	}
	// "planning" -> "plann" -> "plan"
	if n := len(w); n >= 4 && w[n-1] == w[n-2] && !strings.ContainsRune("aeiou", rune(w[n-1])) {
		w = w[:n-1]
	}
	return w
}
