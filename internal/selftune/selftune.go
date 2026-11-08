// Package selftune is the opt-in self-tuning loop (issue #390): from
// explicitly selected past workflow runs (the --workflow -o records) it
// proposes one small, reviewable improvement to a project-owned workflow
// skill or its success check, validates it by re-running the failing
// tasks in isolated workspace copies with and without the change, and
// applies (or reverts) only the exact change a person approved by digest.
//
// It builds on internal/observer: failures are grouped by the observer's
// failure signature, and a candidate records the observer ledger entry
// and any ratchet rule applied for that signature as provenance. There
// is no second learning store: a candidate is one reviewable artifact
// under <cwd>/.tinyagent/observer/candidates/.
//
// Saved runs are untrusted data. The run only names a failure; the file a
// candidate may change is the failed stage's skill file as defined by the
// current, project-owned workflow file inside --cwd, never a path from the
// run. Text taken from a run is redacted, bounded and dropped when it
// looks like instructions. AGENTS.md, CLAUDE.md, config, memory and
// tinyagent state are never targets.
package selftune

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/cwdpath"
)

// RelDir is the candidates directory relative to --cwd.
const RelDir = ".tinyagent/observer/candidates"

// Bounds on what is read and stored.
const (
	// MaxRunBytes caps one run record file.
	MaxRunBytes = 8 << 20
	// MaxRuns caps the runs one proposal reads.
	MaxRuns = 20
	// MaxExcerpt caps a stored check-output excerpt, in bytes.
	MaxExcerpt = 1000
	// MaxReason caps a stored failure reason.
	MaxReason = 300
	// MaxTask caps a stored task; validation reruns it.
	MaxTask = 16 << 10
	// MaxLesson caps the instruction text a candidate adds.
	MaxLesson = 400
	// MaxCandidateBytes caps a candidate file.
	MaxCandidateBytes = 2 << 20
)

// Candidate statuses.
const (
	StatusProposed  = "proposed"
	StatusValidated = "validated"
	StatusRejected  = "rejected"
	StatusApplied   = "applied"
	StatusReverted  = "reverted"
)

// Candidate kinds.
const (
	// KindSkill adds a lesson to the stage's skill instructions.
	KindSkill = "skill"
	// KindCheck makes a model-checked stage's success check stricter.
	KindCheck = "check"
)

// Validation verdicts.
const (
	VerdictAccepted      = "accepted"
	VerdictRegression    = "regression"
	VerdictNoImprovement = "no_improvement"
	VerdictIncomplete    = "incomplete"
)

// LessonsHeading is the skill section skill-kind candidates add to.
const LessonsHeading = "## Lessons from past runs"

// Evidence is one source run.
type Evidence struct {
	Path     string   `json:"path"` // relative to --cwd
	SHA256   string   `json:"sha256"`
	Workflow string   `json:"workflow"`
	Status   string   `json:"status"`
	Stage    string   `json:"stage,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Method   string   `json:"check_method,omitempty"`
	Argv     []string `json:"check_argv,omitempty"`
	Excerpt  string   `json:"check_output_excerpt,omitempty"`
	Task     string   `json:"task"`
}

// ObserverRef is the observer's record of the failure signature.
type ObserverRef struct {
	ArtifactID string `json:"artifact_id,omitempty"`
	Count      int    `json:"count,omitempty"`
	FirstSeen  string `json:"first_seen,omitempty"`
	LastSeen   string `json:"last_seen,omitempty"`
	// RuleID is the ratchet rule applied from the observer's proposal.
	RuleID string `json:"rule_id,omitempty"`
}

// Inference records the optional auxiliary-model call of a proposal.
type Inference struct {
	Model        string  `json:"model"`
	Attempts     int     `json:"attempts"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	// Status is "used", "rejected" (unsafe or malformed text),
	// "over_budget" or "error"; anything but used keeps the
	// deterministic text.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Outcome is one validation run.
type Outcome struct {
	Variant     string  `json:"variant"` // "baseline" or "candidate"
	Task        string  `json:"task"`    // sha256 prefix of the task
	Source      string  `json:"source"`  // the run file the task came from
	Control     bool    `json:"control,omitempty"`
	Status      string  `json:"status"`
	FailedStage string  `json:"failed_stage,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	CostUSD     float64 `json:"cost_usd"`
}

// Validation is the before/after record of one validation.
type Validation struct {
	At           string    `json:"at"`
	Outcomes     []Outcome `json:"outcomes"`
	Runs         int       `json:"runs"`
	CostUSD      float64   `json:"cost_usd"`
	Improvements int       `json:"improvements"`
	Regressions  int       `json:"regressions"`
	Verdict      string    `json:"verdict"`
	Reason       string    `json:"reason"`
}

// Event is one step in a candidate's history.
type Event struct {
	At     string `json:"at"`
	Event  string `json:"event"`
	Detail string `json:"detail,omitempty"`
}

// Candidate is one proposed improvement and everything needed to review,
// validate, apply and revert exactly it.
type Candidate struct {
	ID             string `json:"id"`
	Created        string `json:"created"`
	Signature      string `json:"signature"`
	Workflow       string `json:"workflow"`
	WorkflowFile   string `json:"workflow_file"` // relative to --cwd
	Stage          string `json:"stage"`
	Kind           string `json:"kind"`
	Target         string `json:"target"` // relative to --cwd, slash-separated
	Change         string `json:"change"` // the instruction text added
	Rationale      string `json:"rationale"`
	ExpectedEffect string `json:"expected_effect"`
	BaseSHA256     string `json:"base_sha256"`
	NewSHA256      string `json:"new_sha256"`
	// Digest names this exact change; apply needs it.
	Digest string `json:"digest"`
	Diff   string `json:"diff"`
	Before string `json:"before"`
	After  string `json:"after"`
	// Evidence are the failed runs with the signature; Controls are the
	// selected runs that passed, rerun to catch regressions.
	Evidence   []Evidence   `json:"evidence"`
	Controls   []Evidence   `json:"controls,omitempty"`
	Observer   *ObserverRef `json:"observer,omitempty"`
	Inference  *Inference   `json:"inference,omitempty"`
	Status     string       `json:"status"`
	Validation *Validation  `json:"validation,omitempty"`
	History    []Event      `json:"history"`
}

var reID = regexp.MustCompile(`^t-[0-9a-f]{12}$`)

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// digestOf names a change: its target and the before and after content.
func digestOf(target, base, next string) string {
	return sha(target + "\x00" + base + "\x00" + next)[:12]
}

func idOf(sig, target, base, next string) string {
	return "t-" + sha(sig + "\x00" + target + "\x00" + base + "\x00" + next)[:12]
}

func stamp(now time.Time) string { return now.UTC().Format(time.RFC3339) }

func (c *Candidate) log(now time.Time, ev, detail string) {
	c.History = append(c.History, Event{At: stamp(now), Event: ev, Detail: detail})
}

// check verifies a loaded candidate is internally consistent: the stored
// content matches its hashes and the digest names exactly this change.
func (c *Candidate) check() error {
	switch {
	case !reID.MatchString(c.ID):
		return fmt.Errorf("candidate id %q is malformed", c.ID)
	case sha(c.Before) != c.BaseSHA256 || sha(c.After) != c.NewSHA256:
		return fmt.Errorf("candidate %s: stored content does not match its hashes; it was edited, propose again", c.ID)
	case c.Digest != digestOf(c.Target, c.BaseSHA256, c.NewSHA256):
		return fmt.Errorf("candidate %s: digest does not match its change; it was edited, propose again", c.ID)
	case idOf(c.Signature, c.Target, c.BaseSHA256, c.NewSHA256) != c.ID:
		return fmt.Errorf("candidate %s: id does not match its change; it was edited, propose again", c.ID)
	}
	return checkTarget(c.Target)
}

// Dir returns the candidates directory for root, refusing one that a
// symlink would take outside root.
func Dir(root string) (string, error) { return cwdpath.Resolve(root, RelDir) }

func candidatePath(root, id string) (string, error) {
	if !reID.MatchString(id) {
		return "", fmt.Errorf("no candidate %q: ids look like t-0123456789ab (see tinyagent tune list)", id)
	}
	return cwdpath.Resolve(root, RelDir+"/"+id+".json")
}

// Load reads candidate id under root.
func Load(root, id string) (*Candidate, error) {
	p, err := candidatePath(root, id)
	if err != nil {
		return nil, err
	}
	b, err := readRegular(p, MaxCandidateBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no candidate %q in %s (see tinyagent tune list)", id, RelDir)
	}
	if err != nil {
		return nil, err
	}
	var c Candidate
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("candidate %s: %v", id, err)
	}
	if c.ID != id {
		return nil, fmt.Errorf("candidate file %s holds id %q", p, c.ID)
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	return &c, nil
}

// List reads every candidate under root, oldest first. Unreadable files
// are reported as warnings.
func List(root string) ([]*Candidate, []string, error) {
	dir, err := Dir(root)
	if err != nil {
		return nil, nil, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []*Candidate
	var warns []string
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !reID.MatchString(id) {
			continue
		}
		c, err := Load(root, id)
		if err != nil {
			warns = append(warns, err.Error())
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].ID < out[j].ID
	})
	return out, warns, nil
}

// Save writes c under root atomically.
func Save(root string, c *Candidate) error {
	p, err := candidatePath(root, c.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// Re-check after creating directories: a symlink planted meanwhile
	// must not redirect the write.
	if _, err := candidatePath(root, c.ID); err != nil {
		return err
	}
	return writeAtomic(p, append(b, '\n'), 0o600)
}

// readRegular reads a regular file (not a symlink) of at most n bytes.
func readRegular(p string, n int64) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if fi.Size() > n {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, n)
	}
	return os.ReadFile(p)
}

func writeAtomic(p string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err == nil {
		err = os.Rename(name, p)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// protectedNames are files a candidate may never change, whatever the
// workflow says.
var protectedNames = map[string]bool{
	"agents.md": true, "claude.md": true, "tinyagent.yaml": true, "memory.toml": true, ".env": true,
}

// checkTarget refuses a target that is not a Markdown skill file or is a
// protected file or under tinyagent's own state.
func checkTarget(rel string) error {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("target %q must be a path relative to --cwd", rel)
	}
	for _, el := range strings.Split(rel, "/") {
		switch strings.ToLower(el) {
		case "..":
			return fmt.Errorf("target %q leaves --cwd", rel)
		case ".tinyagent", ".git":
			return fmt.Errorf("target %q is inside %s, which self-tuning never edits", rel, el)
		}
	}
	base := strings.ToLower(filepath.Base(rel))
	if protectedNames[base] {
		return fmt.Errorf("target %q is a protected file; self-tuning never edits AGENTS.md, CLAUDE.md, config, memory or .env", rel)
	}
	if !strings.HasSuffix(base, ".md") {
		return fmt.Errorf("target %q is not a Markdown skill file", rel)
	}
	return nil
}

// resolveTarget is rel's absolute path inside root; it must be a regular
// file (not a symlink) when it exists.
func resolveTarget(root, rel string) (string, error) {
	if err := checkTarget(rel); err != nil {
		return "", err
	}
	p, err := cwdpath.Resolve(root, filepath.FromSlash(rel))
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("target %s is not a regular file (symlinks are refused)", rel)
	}
	return p, nil
}
