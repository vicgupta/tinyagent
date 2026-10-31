// Package harness runs a workflow of minimal markdown skills: a router picks
// the skills for a task, each skill is one narrow prompt with a success
// check, and a loop runs them in order, feeding each stage's output to the
// next and stopping at the first stage that fails its check after retries.
//
// The three layers live in their own files: router.go, skill.go and
// loop.go; workflow.go parses the workflow file that ties them together.
// Each stage is an ordinary single tinyagent run.
package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxSkillBytes caps the size of one skill file.
const MaxSkillBytes = 256 << 10

// Skill is one parsed skill file: a heading, an intent line, declared
// inputs and a one-line success check. Everything else (model, limits,
// retries, check commands) comes from the workflow file's defaults.
type Skill struct {
	Name         string   `json:"name"`
	Title        string   `json:"title,omitempty"`
	Intent       string   `json:"intent"`
	Inputs       []string `json:"inputs"`
	SuccessCheck string   `json:"success_check,omitempty"`
	// Body is the file's Markdown with any YAML frontmatter removed; it
	// is the stage's instructions.
	Body string `json:"-"`
	Path string `json:"path"`
}

var (
	reHeading = regexp.MustCompile(`^#\s+(.+?)\s*$`)
	reIntent  = regexp.MustCompile(`(?i)^>?\s*\**\s*intent\s*\**\s*:\s*\**\s*(.+?)\s*$`)
	reInputs  = regexp.MustCompile(`(?i)^(#{1,6}\s*)?\**\s*inputs\s*:?\s*\**\s*:?\s*$`)
	reCheck   = regexp.MustCompile(`(?i)^\**\s*success check\s*:?\s*\**\s*:?\s*(.+?)\s*$`)
	reBullet  = regexp.MustCompile(`^\s*[-*+]\s+(.+?)\s*$`)
	reName    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// ParseSkill parses a skill file's content. name is the skill's id (its file
// stem, or its folder name for a SKILL.md). The success check may be empty;
// the workflow decides whether a stage needs one.
func ParseSkill(name, path string, data []byte) (*Skill, error) {
	if len(data) > MaxSkillBytes {
		return nil, fmt.Errorf("skill %s: %s is larger than %d bytes", name, path, MaxSkillBytes)
	}
	s := &Skill{Name: name, Path: path}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var front struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if strings.HasPrefix(text, "---\n") {
		end := strings.Index(text[4:], "\n---")
		if end < 0 {
			return nil, fmt.Errorf("skill %s: %s: unterminated frontmatter", name, path)
		}
		if err := yaml.Unmarshal([]byte(text[4:4+end]), &front); err != nil {
			return nil, fmt.Errorf("skill %s: %s: frontmatter: %v", name, path, err)
		}
		text = strings.TrimLeft(text[4+end+4:], "\n")
	}
	s.Body = strings.TrimSpace(text)
	inInputs := false
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if inInputs {
			if m := reBullet.FindStringSubmatch(line); m != nil {
				s.Inputs = append(s.Inputs, inputName(m[1]))
				continue
			}
			if trim == "" && len(s.Inputs) == 0 {
				continue
			}
			inInputs = false
		}
		switch {
		case s.Title == "" && reHeading.MatchString(trim):
			t := reHeading.FindStringSubmatch(trim)[1]
			if after, ok := cutPrefixFold(t, "skill:"); ok {
				t = strings.TrimSpace(after)
			}
			s.Title = t
		case s.Intent == "" && reIntent.MatchString(trim):
			s.Intent = strings.Trim(reIntent.FindStringSubmatch(trim)[1], "* ")
		case reInputs.MatchString(trim):
			inInputs = true
		case s.SuccessCheck == "" && reCheck.MatchString(trim):
			s.SuccessCheck = strings.Trim(reCheck.FindStringSubmatch(trim)[1], "* ")
		}
	}
	if s.Title == "" {
		s.Title = front.Name
	}
	if s.Intent == "" {
		s.Intent = strings.TrimSpace(front.Description)
	}
	var missing []string
	if s.Title == "" {
		missing = append(missing, `a "# Skill: <name>" heading`)
	}
	if s.Intent == "" {
		missing = append(missing, `an "> Intent: ..." line`)
	}
	if len(s.Inputs) == 0 {
		missing = append(missing, `an "Inputs" list with at least one item`)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("skill %s: %s is missing %s", name, path, strings.Join(missing, ", "))
	}
	return s, nil
}

// inputName reduces an input bullet ("`spec`: the feature spec") to its name.
func inputName(b string) string {
	for _, sep := range []string{":", " — ", " - ", " ("} {
		if i := strings.Index(b, sep); i > 0 {
			b = b[:i]
		}
	}
	return strings.Trim(strings.TrimSpace(b), "`*_")
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// LoadSkills reads every skill in dir: <name>.md files and <name>/SKILL.md
// folders. A name defined both ways, an invalid name or an unparseable
// file is an error.
func LoadSkills(dir string) (map[string]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("skills_dir: %v", err)
	}
	out := map[string]*Skill{}
	var errs []error
	for _, e := range entries {
		var name, path string
		switch {
		case e.IsDir():
			p := filepath.Join(dir, e.Name(), "SKILL.md")
			if _, err := os.Stat(p); err != nil {
				continue
			}
			name, path = e.Name(), p
		case strings.HasSuffix(e.Name(), ".md") && !strings.EqualFold(e.Name(), "README.md"):
			name, path = strings.TrimSuffix(e.Name(), ".md"), filepath.Join(dir, e.Name())
		default:
			continue
		}
		if !reName.MatchString(name) {
			errs = append(errs, fmt.Errorf("skill %q: name must be lowercase letters, digits, '.', '_' or '-'", name))
			continue
		}
		if prev, ok := out[name]; ok {
			errs = append(errs, fmt.Errorf("skill %s: defined twice (%s and %s)", name, prev.Path, path))
			continue
		}
		data, err := readCapped(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("skill %s: %v", name, err))
			continue
		}
		s, err := ParseSkill(name, path, data)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[name] = s
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// LoadSkillFile reads and parses one skill file with the same rules
// LoadSkills applies: its name is the file stem, or the folder name for a
// <name>/SKILL.md, and must be a valid skill name.
func LoadSkillFile(path string) (*Skill, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.EqualFold(filepath.Base(path), "SKILL.md") {
		name = filepath.Base(filepath.Dir(path))
	}
	if !reName.MatchString(name) {
		return nil, fmt.Errorf("skill %q: name must be lowercase letters, digits, '.', '_' or '-'", name)
	}
	data, err := readCapped(path)
	if err != nil {
		return nil, fmt.Errorf("skill %s: %v", name, err)
	}
	return ParseSkill(name, path, data)
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, MaxSkillBytes+1)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sortedNames returns the keys of m in order.
func sortedNames[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
