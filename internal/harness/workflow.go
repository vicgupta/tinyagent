package harness

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Defaults and bounds.
const (
	// DefaultRetries is how many times a failed stage is retried.
	DefaultRetries = 1
	// MaxRetries bounds retries per stage.
	MaxRetries = 5
	// DefaultStepLimit is each stage run's --step-limit unless overridden.
	DefaultStepLimit = 50
	// DefaultCheckTimeout bounds a check command, in seconds.
	DefaultCheckTimeout = 300
	// MaxStages bounds a workflow's length.
	MaxStages = 20
	// MaxWorkflowBytes caps the workflow file.
	MaxWorkflowBytes = 256 << 10
)

// Settings are the overridable per-stage settings; zero values inherit.
type Settings struct {
	Model        string   `yaml:"model" json:"model,omitempty"`
	Profile      string   `yaml:"profile" json:"profile,omitempty"`
	StepLimit    *int     `yaml:"step_limit" json:"step_limit,omitempty"`
	CostLimit    *float64 `yaml:"cost_limit" json:"cost_limit,omitempty"`
	TokenLimit   *int     `yaml:"token_limit" json:"token_limit,omitempty"`
	TimeLimit    *int     `yaml:"time_limit" json:"time_limit,omitempty"`
	Retries      *int     `yaml:"retries" json:"retries,omitempty"`
	CheckTimeout *int     `yaml:"check_timeout" json:"check_timeout,omitempty"`
}

// Stage is one resolved workflow step.
type Stage struct {
	Skill *Skill `json:"skill"`
	// Check is a command whose exit status decides the stage's success
	// check; when empty the model's own verdict on the skill's success
	// check line decides.
	Check        []string          `json:"check,omitempty"`
	CheckTimeout int               `json:"check_timeout"`
	Retries      int               `json:"retries"`
	Model        string            `json:"model,omitempty"`
	Profile      string            `json:"profile,omitempty"`
	StepLimit    int               `json:"step_limit"`
	CostLimit    float64           `json:"cost_limit,omitempty"`
	TokenLimit   int               `json:"token_limit,omitempty"`
	TimeLimit    int               `json:"time_limit,omitempty"`
	Inputs       map[string]string `json:"inputs,omitempty"`
}

// Pipeline is one named, resolved workflow.
type Pipeline struct {
	Name   string  `json:"name"`
	Intent string  `json:"intent,omitempty"`
	Stages []Stage `json:"stages"`
}

// Workflow is a parsed, validated workflow file.
type Workflow struct {
	Path      string
	SkillsDir string
	Default   string
	Skills    map[string]*Skill
	Pipelines map[string]*Pipeline
	defaults  Settings
}

type fileStage struct {
	Skill    string            `yaml:"skill"`
	Check    []string          `yaml:"check"`
	Inputs   map[string]string `yaml:"inputs"`
	Settings `yaml:",inline"`
}

type filePipeline struct {
	Intent string      `yaml:"intent"`
	Stages []fileStage `yaml:"stages"`
}

type file struct {
	Version   int                      `yaml:"version"`
	SkillsDir string                   `yaml:"skills_dir"`
	Default   string                   `yaml:"default"`
	Defaults  Settings                 `yaml:"defaults"`
	Workflows map[string]*filePipeline `yaml:"workflows"`
}

var reWorkflowName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Load parses and validates the workflow file at path and every skill in its
// skills directory. Any problem is returned before a model is called.
func Load(path string) (*Workflow, error) {
	data, err := readCappedN(path, MaxWorkflowBytes)
	if err != nil {
		return nil, fmt.Errorf("workflow %s: %v", path, err)
	}
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("workflow %s: %v", path, err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("workflow %s: version must be 1", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	w := &Workflow{Path: abs, Default: f.Default, Pipelines: map[string]*Pipeline{}, defaults: f.Defaults}
	dir := f.SkillsDir
	if dir == "" {
		dir = "skills"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(abs), dir)
	}
	w.SkillsDir = dir
	if err := checkSettings("defaults", f.Defaults); err != nil {
		return nil, fmt.Errorf("workflow %s: %v", path, err)
	}
	if w.Skills, err = LoadSkills(dir); err != nil {
		return nil, fmt.Errorf("workflow %s: %v", path, err)
	}
	if len(w.Skills) == 0 {
		return nil, fmt.Errorf("workflow %s: no skills found in %s", path, dir)
	}
	var errs []error
	for _, name := range sortedNames(f.Workflows) {
		fp := f.Workflows[name]
		if !reWorkflowName.MatchString(name) {
			errs = append(errs, fmt.Errorf("workflow %q: name must be lowercase letters, digits, '.', '_' or '-'", name))
			continue
		}
		if _, clash := w.Skills[name]; clash {
			errs = append(errs, fmt.Errorf("workflow %q: name is also a skill name", name))
			continue
		}
		if fp == nil || len(fp.Stages) == 0 {
			errs = append(errs, fmt.Errorf("workflow %q: needs at least one stage", name))
			continue
		}
		if len(fp.Stages) > MaxStages {
			errs = append(errs, fmt.Errorf("workflow %q: at most %d stages", name, MaxStages))
			continue
		}
		p := &Pipeline{Name: name, Intent: strings.TrimSpace(fp.Intent)}
		for i, fs := range fp.Stages {
			st, err := w.resolve(fs)
			if err != nil {
				errs = append(errs, fmt.Errorf("workflow %q stage %d: %v", name, i+1, err))
				continue
			}
			p.Stages = append(p.Stages, st)
		}
		w.Pipelines[name] = p
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("workflow %s: %w", path, errors.Join(errs...))
	}
	if w.Default != "" {
		if _, ok := w.Pipelines[w.Default]; !ok {
			if _, ok := w.Skills[w.Default]; !ok {
				return nil, fmt.Errorf("workflow %s: default %q is not a workflow or skill", path, w.Default)
			}
		}
	}
	return w, nil
}

func (w *Workflow) resolve(fs fileStage) (Stage, error) {
	sk, ok := w.Skills[fs.Skill]
	if !ok {
		if fs.Skill == "" {
			return Stage{}, errors.New("skill is required")
		}
		return Stage{}, fmt.Errorf("unknown skill %q (known: %s)", fs.Skill, strings.Join(sortedNames(w.Skills), ", "))
	}
	if err := checkSettings("stage", fs.Settings); err != nil {
		return Stage{}, err
	}
	if fs.Check != nil && (len(fs.Check) == 0 || fs.Check[0] == "") {
		return Stage{}, errors.New("check must be a non-empty argv list")
	}
	if sk.SuccessCheck == "" && len(fs.Check) == 0 {
		return Stage{}, fmt.Errorf("skill %s has no \"Success check:\" line, so the stage needs a check command", sk.Name)
	}
	st := w.single(sk)
	st.Check = fs.Check
	st.Inputs = fs.Inputs
	apply(&st, fs.Settings)
	return st, nil
}

// single returns a stage running sk with the file's defaults.
func (w *Workflow) single(sk *Skill) Stage {
	st := Stage{Skill: sk, Retries: DefaultRetries, StepLimit: DefaultStepLimit, CheckTimeout: DefaultCheckTimeout}
	apply(&st, w.defaults)
	return st
}

func apply(st *Stage, s Settings) {
	if s.Model != "" {
		st.Model = s.Model
	}
	if s.Profile != "" {
		st.Profile = s.Profile
	}
	if s.StepLimit != nil {
		st.StepLimit = *s.StepLimit
	}
	if s.CostLimit != nil {
		st.CostLimit = *s.CostLimit
	}
	if s.TokenLimit != nil {
		st.TokenLimit = *s.TokenLimit
	}
	if s.TimeLimit != nil {
		st.TimeLimit = *s.TimeLimit
	}
	if s.Retries != nil {
		st.Retries = *s.Retries
	}
	if s.CheckTimeout != nil {
		st.CheckTimeout = *s.CheckTimeout
	}
}

func checkSettings(where string, s Settings) error {
	switch {
	case s.StepLimit != nil && *s.StepLimit < 0:
		return fmt.Errorf("%s: step_limit must be >= 0", where)
	case s.CostLimit != nil && *s.CostLimit < 0:
		return fmt.Errorf("%s: cost_limit must be >= 0", where)
	case s.TokenLimit != nil && *s.TokenLimit < 0:
		return fmt.Errorf("%s: token_limit must be >= 0", where)
	case s.TimeLimit != nil && *s.TimeLimit < 0:
		return fmt.Errorf("%s: time_limit must be >= 0", where)
	case s.Retries != nil && (*s.Retries < 0 || *s.Retries > MaxRetries):
		return fmt.Errorf("%s: retries must be between 0 and %d", where, MaxRetries)
	case s.CheckTimeout != nil && *s.CheckTimeout <= 0:
		return fmt.Errorf("%s: check_timeout must be > 0", where)
	}
	return nil
}

func readCappedN(path string, n int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > n {
		return nil, fmt.Errorf("larger than %d bytes", n)
	}
	return os.ReadFile(path)
}
