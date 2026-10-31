// Package observer turns a failed or blocked skill-workflow run into one
// concrete, reviewable artifact: a proposed project rule (see
// internal/ratchet) with a ready-to-run `tinyagent ratchet add` command.
// It also keeps a ledger of failure signatures so a failure that recurs
// after a proposal exists is flagged: the gate did not hold.
//
// The observer is deterministic (no model call) and never applies a rule
// itself; a person runs the printed command.
package observer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/harness"
	"github.com/vicgupta/tinyagent/internal/ratchet"
)

// MaxExcerpt caps the stored check output excerpt, in bytes.
const MaxExcerpt = 2000

// Dir is the observer directory under the project root.
func Dir(root string) string { return filepath.Join(root, ".tinyagent", "observer") }

// ProposalsPath is the proposals file under the project root.
func ProposalsPath(root string) string { return filepath.Join(Dir(root), "proposals.jsonl") }

// LedgerPath is the ledger file under the project root.
func LedgerPath(root string) string { return filepath.Join(Dir(root), "ledger.jsonl") }

// Proposal is one proposed project rule distilled from a failed run.
type Proposal struct {
	ID        string   `json:"id"`
	Signature string   `json:"signature"`
	Created   string   `json:"created"`
	Workflow  string   `json:"workflow"`
	Status    string   `json:"status"`
	Stage     string   `json:"stage"`
	Reason    string   `json:"reason,omitempty"`
	Method    string   `json:"check_method,omitempty"`
	Argv      []string `json:"check_argv,omitempty"`
	Excerpt   string   `json:"check_output_excerpt,omitempty"`
	Rule      string   `json:"rule"`
	Incident  string   `json:"incident"`
	Command   string   `json:"suggested_command"`
}

// Entry is one ledger line. The latest line for a signature is its state.
type Entry struct {
	Signature  string `json:"signature"`
	Stage      string `json:"stage"`
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	Count      int    `json:"count"`
	ArtifactID string `json:"artifact_id"`
}

// Observation is what Observe did for one run.
type Observation struct {
	// Observed is false for a passed or interrupted run: nothing written.
	Observed bool
	Proposal *Proposal // set on the first occurrence of a signature
	Entry    Entry
	// Recurrence is set when the signature was already in the ledger.
	Recurrence bool
	// RuleID is the applied ratchet rule for this signature, if any.
	RuleID string
	// Warnings are non-fatal problems, such as a corrupt ledger.
	Warnings []string
}

// failedAttempt is the last attempt of the failed stage.
func failedAttempt(res *harness.Result) *harness.Attempt {
	for i := len(res.Attempts) - 1; i >= 0; i-- {
		if res.Attempts[i].Skill == res.FailedStage {
			return &res.Attempts[i]
		}
	}
	return nil
}

var (
	reFailLine = regexp.MustCompile(`(?i)\b(fail|failed|failure|error|panic|fatal|undefined|cannot|can't|expected)\b`)
	rePath     = regexp.MustCompile(`(?:[A-Za-z]:)?(?:[\w.~-]*[/\\])+[\w.-]*`)
	reHex      = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b|\b[0-9a-f]{7,}\b`)
	reNum      = regexp.MustCompile(`\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)?`)
	reSpace    = regexp.MustCompile(`\s+`)
)

// Normalize strips the parts of a line that change between runs of the
// same failure: paths, hashes, numbers and durations.
func Normalize(s string) string {
	s = rePath.ReplaceAllString(s, "<path>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reNum.ReplaceAllString(s, "<n>")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// firstFailingLine is the first line of out that looks like a failure, else
// the first non-blank line.
func firstFailingLine(out string) string {
	first := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if first == "" {
			first = l
		}
		if reFailLine.MatchString(l) {
			return l
		}
	}
	return first
}

// Signature identifies a failure across runs: sha256 of the failed stage,
// the check argv and the normalized first failing line of the check
// output (the reason when there is no output).
func Signature(res *harness.Result) string {
	var argv []string
	line := ""
	if at := failedAttempt(res); at != nil {
		argv = at.Check.Argv
		line = firstFailingLine(at.Check.Output)
	}
	if line == "" {
		line = firstFailingLine(res.Reason)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", res.FailedStage, strings.Join(argv, "\x1f"), Normalize(line))
	return hex.EncodeToString(h.Sum(nil))
}

func oneLine(s string, n int) string {
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// tag is the marker an incident carries so a recurrence can find the rule.
func tag(sig string) string { return "observer " + sig[:12] }

// Propose builds the proposal for a failed run. redact masks secrets in
// stored text; nil stores it as is.
func Propose(res *harness.Result, root string, redact func(string) string, now time.Time) Proposal {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	sig := Signature(res)
	p := Proposal{
		ID: "p-" + sig[:12], Signature: sig, Created: now.Format(ratchet.DateLayout),
		Workflow: res.Workflow, Status: res.Status, Stage: res.FailedStage,
		Reason: oneLine(redact(res.Reason), 300),
	}
	if at := failedAttempt(res); at != nil {
		p.Method = at.Check.Method
		p.Argv = at.Check.Argv
		ex := redact(at.Check.Output)
		if len(ex) > MaxExcerpt {
			ex = ex[:MaxExcerpt] + "\n[truncated]"
		}
		p.Excerpt = ex
	}
	if len(p.Argv) > 0 {
		p.Rule = fmt.Sprintf("Before finishing the %s stage, run `%s` yourself and fix what it reports until it passes", p.Stage, redact(strings.Join(p.Argv, " ")))
	} else {
		p.Rule = fmt.Sprintf("Before finishing the %s stage, check the work against this past failure: %s", p.Stage, oneLine(p.Reason, 200))
	}
	p.Incident = fmt.Sprintf("workflow %s %s at stage %s on %s: %s (%s)", p.Workflow, p.Status, p.Stage, p.Created, oneLine(p.Reason, 200), tag(sig))
	p.Command = fmt.Sprintf("tinyagent ratchet add %s --incident %s --cwd %s", shellQuote(p.Rule), shellQuote(p.Incident), shellQuote(root))
	return p
}

// LoadLedger reads the ledger: the latest entry per signature. A corrupt
// line is skipped and reported, never fatal.
func LoadLedger(path string) (map[string]Entry, []string) {
	m := map[string]Entry{}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return m, []string{fmt.Sprintf("observer: can't read ledger %s: %v; starting fresh", path, err)}
	}
	var warns []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) != nil || e.Signature == "" {
			warns = append(warns, fmt.Sprintf("observer: skipping corrupt ledger line %d in %s", n, path))
			continue
		}
		m[e.Signature] = e
	}
	return m, warns
}

// appendLine appends v as one JSON line with a single O_APPEND write, so
// concurrent runs in the same project don't interleave lines.
func appendLine(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// appliedRule finds the ratchet rule created from this signature's
// proposal, by the observer tag in its incident.
func appliedRule(root, sig string) string {
	rules, err := ratchet.Load(ratchet.Path(root))
	if err != nil {
		return ""
	}
	for _, r := range rules {
		if strings.Contains(r.Incident, tag(sig)) {
			return r.ID
		}
	}
	return ""
}

// Observe records a failed or blocked run under root/.tinyagent/observer:
// a new signature gets one proposal and a ledger entry; a known signature
// bumps its count and is reported as a recurrence. Passed and interrupted
// runs are not observed. An error means a file couldn't be written; the
// caller should warn, not fail the run.
func Observe(res *harness.Result, root string, redact func(string) string, now time.Time) (Observation, error) {
	var ob Observation
	if res == nil || (res.Status != harness.StatusFailed && res.Status != harness.StatusBlocked) {
		return ob, nil
	}
	ob.Observed = true
	sig := Signature(res)
	ledger, warns := LoadLedger(LedgerPath(root))
	ob.Warnings = warns
	ts := now.UTC().Format(time.RFC3339)
	e, seen := ledger[sig]
	if seen {
		e.Count++
		e.LastSeen = ts
		ob.Recurrence = true
		ob.RuleID = appliedRule(root, sig)
	} else {
		p := Propose(res, root, redact, now)
		if err := appendLine(ProposalsPath(root), p); err != nil {
			return ob, err
		}
		ob.Proposal = &p
		e = Entry{Signature: sig, Stage: res.FailedStage, FirstSeen: ts, LastSeen: ts, Count: 1, ArtifactID: p.ID}
	}
	ob.Entry = e
	return ob, appendLine(LedgerPath(root), e)
}
