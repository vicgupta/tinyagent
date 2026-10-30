// Package eval runs a suite of coding tasks against tinyagent and reports
// how often each model/profile configuration passes the task's own checks.
// See docs/eval-framework.md for the design.
package eval

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultCheckTimeout bounds a check that does not set timeout_seconds.
const DefaultCheckTimeout = 300

// Suite is a parsed, validated suite file.
type Suite struct {
	Version  int      `yaml:"version"`
	Models   []string `yaml:"models"`
	Profiles []string `yaml:"profiles"`
	Trials   int      `yaml:"trials"`
	Tasks    []Task   `yaml:"tasks"`

	dir            string // directory of the suite file; relative paths resolve here
	defaultProfile bool   // the config defines a profile literally named "default"
}

// Task is one coding task: a repo at a base commit, a prompt, and checks.
type Task struct {
	ID            string   `yaml:"id"`
	Repo          string   `yaml:"repo"`
	Base          string   `yaml:"base"`
	Task          string   `yaml:"task"`
	TaskFile      string   `yaml:"task_file"`
	Model         string   `yaml:"model"`
	Profile       string   `yaml:"profile"`
	Checks        []Check  `yaml:"checks"`
	ChecksOnLimit *bool    `yaml:"checks_on_limit"`
	Limits        Limits   `yaml:"limits"`
	Tags          []string `yaml:"tags"`

	repoAbs    string // absolute repo path
	baseCommit string // resolved full SHA
	prompt     string // task text (inline or from task_file)
}

// Check is one command run in the trial's worktree; exit 0 means pass.
type Check struct {
	Argv           []string `yaml:"argv"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
}

// Limits are per-trial run limits passed to the child tinyagent.
type Limits struct {
	StepLimit  int     `yaml:"step_limit"`
	CostLimit  float64 `yaml:"cost_limit"`
	TokenLimit int     `yaml:"token_limit"` // input+output tokens
	TimeLimit  int     `yaml:"time_limit"`  // seconds
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Load reads and validates a suite file. knownProfile reports whether a
// profile name exists in the user's config; "default" is always accepted
// and means the base config.
func Load(path string, knownProfile func(string) bool) (*Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Suite
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("suite %s: empty file", path)
		}
		return nil, fmt.Errorf("suite %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s.dir = filepath.Dir(abs)
	if err := s.validate(knownProfile); err != nil {
		return nil, fmt.Errorf("suite %s: %w", path, err)
	}
	return &s, nil
}

func (s *Suite) validate(knownProfile func(string) bool) error {
	if s.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", s.Version)
	}
	if s.Trials < 1 {
		return errors.New("trials is required and must be >= 1")
	}
	if len(s.Models) == 0 {
		return errors.New("models: at least one model spec is required")
	}
	if err := uniqueNonEmpty("models", s.Models); err != nil {
		return err
	}
	if len(s.Profiles) == 0 {
		s.Profiles = []string{"default"}
	}
	if err := uniqueNonEmpty("profiles", s.Profiles); err != nil {
		return err
	}
	s.defaultProfile = knownProfile != nil && knownProfile("default")
	checkProfile := func(where, p string) error {
		if p == "default" || knownProfile == nil || knownProfile(p) {
			return nil
		}
		return fmt.Errorf("%s: unknown profile %q", where, p)
	}
	for _, p := range s.Profiles {
		if err := checkProfile("profiles", p); err != nil {
			return err
		}
	}
	if len(s.Tasks) == 0 {
		return errors.New("tasks: at least one task is required")
	}
	seen := map[string]bool{}
	for i := range s.Tasks {
		t := &s.Tasks[i]
		where := fmt.Sprintf("tasks[%d]", i)
		if !idRe.MatchString(t.ID) {
			return fmt.Errorf("%s: id %q must be non-empty and use only letters, digits, '.', '_' or '-'", where, t.ID)
		}
		where = fmt.Sprintf("task %q", t.ID)
		if seen[t.ID] {
			return fmt.Errorf("%s: duplicate id", where)
		}
		seen[t.ID] = true
		if t.Profile != "" {
			if err := checkProfile(where, t.Profile); err != nil {
				return err
			}
		}
		switch {
		case t.Task != "" && t.TaskFile != "":
			return fmt.Errorf("%s: set task or task_file, not both", where)
		case t.Task != "":
			t.prompt = t.Task
		case t.TaskFile != "":
			b, err := os.ReadFile(s.resolve(t.TaskFile))
			if err != nil {
				return fmt.Errorf("%s: task_file: %w", where, err)
			}
			t.prompt = string(b)
		}
		if strings.TrimSpace(t.prompt) == "" {
			return fmt.Errorf("%s: task or task_file is required", where)
		}
		if len(t.Checks) == 0 {
			return fmt.Errorf("%s: at least one check is required", where)
		}
		for j := range t.Checks {
			c := &t.Checks[j]
			if len(c.Argv) == 0 || c.Argv[0] == "" {
				return fmt.Errorf("%s: checks[%d]: argv must be a non-empty list", where, j)
			}
			if c.TimeoutSeconds < 0 {
				return fmt.Errorf("%s: checks[%d]: timeout_seconds must be >= 0", where, j)
			}
			if c.TimeoutSeconds == 0 {
				c.TimeoutSeconds = DefaultCheckTimeout
			}
		}
		if t.Limits.StepLimit < 0 || t.Limits.CostLimit < 0 || t.Limits.TokenLimit < 0 || t.Limits.TimeLimit < 0 {
			return fmt.Errorf("%s: limits must be >= 0", where)
		}
		if t.Repo == "" {
			return fmt.Errorf("%s: repo is required", where)
		}
		if t.Base == "" {
			return fmt.Errorf("%s: base is required (a commit, tag or branch)", where)
		}
		t.repoAbs = s.resolve(t.Repo)
		top, err := gitOut(t.repoAbs, "rev-parse", "--show-toplevel")
		if err != nil {
			return fmt.Errorf("%s: repo %s is not a Git repository", where, t.Repo)
		}
		t.repoAbs = top
		sha, err := gitOut(t.repoAbs, "rev-parse", "--verify", "--quiet", t.Base+"^{commit}")
		if err != nil {
			return fmt.Errorf("%s: base %q does not resolve to a commit in %s", where, t.Base, t.Repo)
		}
		t.baseCommit = sha
	}
	ids := map[string]bool{}
	for _, tr := range s.Expand() {
		if ids[tr.ID] {
			return fmt.Errorf("trial id %q is ambiguous; rename a task, model or profile", tr.ID)
		}
		ids[tr.ID] = true
	}
	return nil
}

func (s *Suite) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.dir, p)
}

func uniqueNonEmpty(field string, vals []string) error {
	seen := map[string]bool{}
	for _, v := range vals {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s: empty entry", field)
		}
		if seen[v] {
			return fmt.Errorf("%s: duplicate entry %q", field, v)
		}
		seen[v] = true
	}
	return nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Trial is one expanded (task, model, profile, n) cell of the matrix.
type Trial struct {
	ID      string
	Task    *Task
	Model   string
	Profile string
	N       int // 1-based trial number
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Expand returns the trial matrix in a stable order: task, model, profile,
// trial number. A task's own model/profile replaces the suite list.
func (s *Suite) Expand() []Trial {
	var out []Trial
	for i := range s.Tasks {
		t := &s.Tasks[i]
		models, profiles := s.Models, s.Profiles
		if t.Model != "" {
			models = []string{t.Model}
		}
		if t.Profile != "" {
			profiles = []string{t.Profile}
		}
		for _, m := range models {
			for _, p := range profiles {
				for n := 1; n <= s.Trials; n++ {
					id := fmt.Sprintf("%s.%s.%s.%d", t.ID, slugRe.ReplaceAllString(m, "-"), slugRe.ReplaceAllString(p, "-"), n)
					out = append(out, Trial{ID: id, Task: t, Model: m, Profile: p, N: n})
				}
			}
		}
	}
	return out
}

// checksOnLimit reports whether checks run after a limit stop (default true).
func (t *Task) checksOnLimit() bool { return t.ChecksOnLimit == nil || *t.ChecksOnLimit }
