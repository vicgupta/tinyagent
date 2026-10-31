// Package triage ranks open issues by value and effort with fixed,
// documented rules, so the output is deterministic and every rank comes with
// its reasons. It makes no model or network calls; the caller supplies the
// issues (for example from gh issue list) and the current time.
package triage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the version of the next-up document written by Encode.
const SchemaVersion = 1

// DuplicateThreshold is the title word overlap (Jaccard index of content
// words) at or above which a newer issue is flagged as a likely duplicate
// of an older one.
const DuplicateThreshold = 0.6

// DefaultStaleDays is the default age, since the last update, after which an
// issue is flagged stale.
const DefaultStaleDays = 30

// BlockedLabels mark an issue as parked (see docs/project-context.md
// "Blocked tasks"); matched case-insensitively.
var BlockedLabels = []string{"amf:blocked", "blocked"}

// valueLabels map a label (lowercase) to its value score. An issue takes the
// highest score among its labels, or defaultValue when none match.
var valueLabels = map[string]int{
	"security":      4,
	"bug":           3,
	"regression":    3,
	"enhancement":   2,
	"feature":       2,
	"documentation": 1,
	"docs":          1,
	"question":      0,
}

const defaultValue = 1

// Label is an issue label. It decodes from either a string or an object with
// a "name" field (the shape gh issue list --json labels prints).
type Label struct {
	Name string `json:"name"`
}

func (l *Label) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &l.Name)
	}
	var v struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	l.Name = v.Name
	return nil
}

// Issue is one input item, in the shape of
// gh issue list --state open --json number,title,body,labels,createdAt,updatedAt.
// PullRequest is set by the GitHub REST issues API for pull requests.
type Issue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Labels      []Label         `json:"labels"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	PullRequest json.RawMessage `json:"pull_request,omitempty"`
	IsPR        bool            `json:"isPullRequest,omitempty"`
}

// Ranked is one entry of the next-up list.
type Ranked struct {
	Rank    int      `json:"rank"`
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Score   float64  `json:"score"`
	Value   int      `json:"value"`
	Effort  int      `json:"effort"`
	Reasons []string `json:"reasons"`
	Flags   []string `json:"flags"`
}

// Options tune Rank.
type Options struct {
	// StaleDays flags issues not updated for more than this many days; 0
	// disables the stale check.
	StaleDays int
}

// Document is the next-up file written by Encode.
type Document struct {
	SchemaVersion int      `json:"schema_version"`
	StaleDays     int      `json:"stale_days"`
	Items         []Ranked `json:"items"`
}

// Decode reads a JSON array of issues.
func Decode(b []byte) ([]Issue, error) {
	var issues []Issue
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&issues); err != nil {
		return nil, fmt.Errorf("issues file is not a JSON array of issues: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("issues file has trailing data after the JSON array")
	}
	seen := map[int]bool{}
	for i, is := range issues {
		if is.Number <= 0 {
			return nil, fmt.Errorf("issue %d in the list has no positive \"number\"", i+1)
		}
		if seen[is.Number] {
			return nil, fmt.Errorf("issue #%d appears more than once", is.Number)
		}
		seen[is.Number] = true
	}
	return issues, nil
}

// Rank scores every issue and returns them all, best first. Nothing is
// dropped: duplicates, blocked items and pull requests stay in the list with
// flags, ranked after the rest. Ties break by issue number ascending.
func Rank(issues []Issue, now time.Time, opts Options) []Ranked {
	type row struct {
		Ranked
		tier int
	}
	rows := make([]row, len(issues))
	words := make([]map[string]bool, len(issues))
	for i, is := range issues {
		words[i] = contentWords(is.Title)
		r := row{Ranked: Ranked{Number: is.Number, Title: is.Title, Flags: []string{}}}
		v, vr := value(is)
		e, er := effort(is)
		r.Value, r.Effort = v, e
		r.Score = math.Round(float64(v)/float64(e)*100) / 100
		r.Reasons = append([]string{fmt.Sprintf("score %.2f = value %d / effort %d", r.Score, v, e)}, vr)
		r.Reasons = append(r.Reasons, er...)
		if opts.StaleDays > 0 {
			last := is.UpdatedAt
			if last.IsZero() {
				last = is.CreatedAt
			}
			if !last.IsZero() {
				if days := int(now.Sub(last).Hours() / 24); days > opts.StaleDays {
					r.Flags = append(r.Flags, "stale")
					r.Reasons = append(r.Reasons, fmt.Sprintf("stale: not updated for %d days (over %d)", days, opts.StaleDays))
				}
			}
		}
		if l := blockedLabel(is); l != "" {
			r.Flags = append(r.Flags, "blocked")
			r.Reasons = append(r.Reasons, "blocked: label "+l+"; ranked last")
			r.tier = 2
		}
		if is.IsPR || (len(is.PullRequest) > 0 && string(is.PullRequest) != "null") {
			r.Flags = append(r.Flags, "pull-request")
			r.Reasons = append(r.Reasons, "pull request, not an issue; ranked last")
			r.tier = 2
		}
		rows[i] = r
	}
	// Likely duplicates: the newer issue (higher number) of a pair whose
	// titles overlap enough points at the oldest such match.
	order := make([]int, len(issues))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return issues[order[a]].Number < issues[order[b]].Number })
	for bi, b := range order {
		for _, a := range order[:bi] {
			if j, ok := jaccard(words[a], words[b]); ok && j >= DuplicateThreshold {
				rows[b].Flags = append(rows[b].Flags, fmt.Sprintf("duplicate-of:#%d", issues[a].Number))
				rows[b].Reasons = append(rows[b].Reasons, fmt.Sprintf("likely duplicate of #%d (title overlap %.2f >= %.2f); ranked after unique issues", issues[a].Number, j, DuplicateThreshold))
				if rows[b].tier < 1 {
					rows[b].tier = 1
				}
				break
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Number < b.Number
	})
	out := make([]Ranked, len(rows))
	for i, r := range rows {
		r.Rank = i + 1
		out[i] = r.Ranked
	}
	return out
}

func value(is Issue) (int, string) {
	best, from := -1, ""
	for _, l := range is.Labels {
		n := strings.ToLower(strings.TrimSpace(l.Name))
		if v, ok := valueLabels[n]; ok && v > best {
			best, from = v, l.Name
		}
	}
	if best < 0 {
		return defaultValue, fmt.Sprintf("value %d: no value label (security, bug, regression, enhancement, feature, documentation, docs, question)", defaultValue)
	}
	return best, fmt.Sprintf("value %d: label %s", best, from)
}

var (
	reHeading  = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*)$`)
	reListItem = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*+])\s+\S`)
)

// effort is 1 to 5: 1, plus 1 for a body over 2000 characters and another
// over 6000, plus 1 for more than 5 acceptance criteria, plus 1 when the
// issue has no "Implementation notes" section.
func effort(is Issue) (int, []string) {
	e := 1
	var reasons []string
	n := len([]rune(is.Body))
	switch {
	case n > 6000:
		e += 2
		reasons = append(reasons, fmt.Sprintf("effort +2: body %d characters (over 6000)", n))
	case n > 2000:
		e++
		reasons = append(reasons, fmt.Sprintf("effort +1: body %d characters (over 2000)", n))
	}
	criteria, notes := scanBody(is.Body)
	if criteria > 5 {
		e++
		reasons = append(reasons, fmt.Sprintf("effort +1: %d acceptance criteria (over 5)", criteria))
	}
	if !notes {
		e++
		reasons = append(reasons, "effort +1: no \"Implementation notes\" section")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "effort 1: short body, at most 5 acceptance criteria, has implementation notes")
	}
	return e, reasons
}

// scanBody counts list items under the largest "Acceptance criteria"
// section and reports whether an "Implementation notes" heading exists.
func scanBody(body string) (criteria int, notes bool) {
	in, cur := false, 0
	for _, line := range strings.Split(body, "\n") {
		if m := reHeading.FindStringSubmatch(line); m != nil {
			h := strings.ToLower(m[1])
			if strings.Contains(h, "implementation notes") {
				notes = true
			}
			in = strings.Contains(h, "acceptance criteria")
			cur = 0
			continue
		}
		if in && reListItem.MatchString(line) {
			cur++
			if cur > criteria {
				criteria = cur
			}
		}
	}
	return criteria, notes
}

func blockedLabel(is Issue) string {
	for _, l := range is.Labels {
		for _, b := range BlockedLabels {
			if strings.EqualFold(strings.TrimSpace(l.Name), b) {
				return l.Name
			}
		}
	}
	return ""
}

// jaccard returns |a∩b| / |a∪b|; ok is false when either title has fewer
// than two content words, too few to call a duplicate.
func jaccard(a, b map[string]bool) (float64, bool) {
	if len(a) < 2 || len(b) < 2 {
		return 0, false
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union), true
}

// Encode writes the next-up document as indented JSON with a trailing
// newline.
func Encode(ranked []Ranked, opts Options) ([]byte, error) {
	if ranked == nil {
		ranked = []Ranked{}
	}
	b, err := json.MarshalIndent(Document{SchemaVersion: SchemaVersion, StaleDays: opts.StaleDays, Items: ranked}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Markdown renders the ranked list as a table for people.
func Markdown(ranked []Ranked) string {
	var sb strings.Builder
	sb.WriteString("| Rank | Issue | Score | Value | Effort | Flags | Title |\n|---|---|---|---|---|---|---|\n")
	for _, r := range ranked {
		flags := strings.Join(r.Flags, ", ")
		title := strings.ReplaceAll(r.Title, "|", "\\|")
		fmt.Fprintf(&sb, "| %d | #%d | %.2f | %d | %d | %s | %s |\n", r.Rank, r.Number, r.Score, r.Value, r.Effort, flags, title)
	}
	return sb.String()
}
