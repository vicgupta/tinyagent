// Package playbooks discovers, parses and routes playbooks: short Markdown
// checklists for a kind of task (fix a bug, open a PR, ...) that a run can
// follow. A playbook's steps go into the system prompt as a checklist, the
// skills it names load with it, and its done check joins verify-on-stop.
//
// Playbooks and skills are read only from inside the current working
// directory: discovery reads ./playbooks in --cwd and nothing else, and
// every path is checked with package cwdpath.
package playbooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vicgupta/tinyagent/internal/cwdpath"
)

// Formats of a playbook file.
const (
	FormatTinyAgent = "tinyagent"
	FormatPstack    = "pstack"
)

// MaxFileBytes caps one playbook file.
const MaxFileBytes = 256 << 10

// MaxSkillBytes caps the instructions of one auto-loaded skill.
const MaxSkillBytes = 32 << 10

// Playbook is one parsed playbook.
type Playbook struct {
	Name     string
	Intent   string
	Match    []string
	Skills   []string
	ReadOnly bool
	Workflow string
	Steps    []string
	Done     string
	// Checks are the done check's commands (lines starting with "$ "),
	// appended to verify_on_stop.checks.
	Checks []string

	Path      string // the playbook file
	Format    string // FormatTinyAgent or FormatPstack
	SkillsDir string // where its skills load from
	raw       []byte
}

// Frontmatter is the TinyAgent format's YAML header.
type Frontmatter struct {
	Name     string   `yaml:"name"`
	Intent   string   `yaml:"intent"`
	Match    []string `yaml:"match"`
	Skills   []string `yaml:"skills"`
	ReadOnly bool     `yaml:"read_only"`
	Workflow string   `yaml:"workflow"`
}

var reName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Dir is the playbooks directory of root: ./playbooks.
func Dir(root string) string { return filepath.Join(root, "playbooks") }

// Parse reads a TinyAgent-format playbook. name is the name the file's
// location implies (<name>.md or <name>/PLAYBOOK.md); "" skips that check.
func Parse(path, name string, data []byte) (*Playbook, error) {
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var f Frontmatter
	dec := yaml.NewDecoder(bytes.NewReader(fm))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("frontmatter: %v", err)
	}
	p := &Playbook{Name: strings.TrimSpace(f.Name), Intent: strings.TrimSpace(f.Intent), Match: f.Match, Skills: f.Skills,
		ReadOnly: f.ReadOnly, Workflow: strings.TrimSpace(f.Workflow), Path: path, Format: FormatTinyAgent, raw: data}
	switch {
	case p.Name == "":
		return nil, errors.New("frontmatter: name is required")
	case !reName.MatchString(p.Name):
		return nil, fmt.Errorf("name %q: use lowercase letters, digits and hyphens", p.Name)
	case name != "" && p.Name != name:
		return nil, fmt.Errorf("name %q doesn't match its file (%s)", p.Name, name)
	case p.Intent == "":
		return nil, errors.New("frontmatter: intent is required")
	}
	for _, s := range p.Skills {
		if !reName.MatchString(s) {
			return nil, fmt.Errorf("skills: %q is not a skill name (no paths)", s)
		}
	}
	if p.Workflow != "" && (filepath.IsAbs(p.Workflow) || hasDotDot(p.Workflow)) {
		return nil, fmt.Errorf("workflow %q: must be a path inside the working directory (no absolute path or ..)", p.Workflow)
	}
	p.Steps, p.Done = parseBody(string(body))
	if len(p.Steps) == 0 {
		return nil, errors.New("body: no steps (a numbered or bulleted list)")
	}
	if p.Done == "" {
		return nil, errors.New(`body: no done check (a "## Done when" section)`)
	}
	p.Checks = doneChecks(p.Done)
	return p, nil
}

func hasDotDot(p string) bool {
	for _, e := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if e == ".." {
			return true
		}
	}
	return false
}

func splitFrontmatter(data []byte) (fm, body []byte, err error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, nil, errors.New("no YAML frontmatter (the file must start with ---)")
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return []byte(rest[:len(rest)-4]), nil, nil
		}
		return nil, nil, errors.New("frontmatter is not closed with ---")
	}
	return []byte(rest[:end]), []byte(rest[end+5:]), nil
}

var (
	reItem    = regexp.MustCompile(`^(\d+[.)]|[-*])\s+(\[[ xX]\]\s+)?(.+)$`)
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reDone    = regexp.MustCompile(`(?i)^done\b`)
)

// parseBody returns the top-level list items before the done section, and
// the done section's text.
func parseBody(body string) (steps []string, done string) {
	var doneLines []string
	inDone, doneLevel := false, 0
	for _, line := range strings.Split(body, "\n") {
		if m := reHeading.FindStringSubmatch(line); m != nil {
			if inDone && len(m[1]) <= doneLevel {
				inDone = false
			}
			if reDone.MatchString(strings.TrimSpace(m[2])) {
				inDone, doneLevel = true, len(m[1])
				continue
			}
		}
		if inDone {
			doneLines = append(doneLines, line)
			continue
		}
		if m := reItem.FindStringSubmatch(line); m != nil {
			steps = append(steps, strings.TrimSpace(m[3]))
		} else if len(steps) > 0 && strings.TrimSpace(line) != "" && (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) {
			steps[len(steps)-1] += " " + strings.TrimSpace(line)
		}
	}
	return steps, strings.TrimSpace(strings.Join(doneLines, "\n"))
}

func doneChecks(done string) []string {
	var out []string
	for _, l := range strings.Split(done, "\n") {
		l = strings.TrimSpace(l)
		if c, ok := strings.CutPrefix(l, "$ "); ok && strings.TrimSpace(c) != "" {
			out = append(out, strings.TrimSpace(c))
		}
	}
	return out
}

// Discover reads the playbooks in root/playbooks: TinyAgent-format
// <name>.md and <name>/PLAYBOOK.md, and pstack checkouts. Unreadable or
// invalid files, unknown layouts and paths that leave root are skipped
// with a warning. On a name clash a TinyAgent-format playbook beats a
// pstack one, and otherwise the first (by path) wins, with a warning.
func Discover(root string) ([]*Playbook, []string) {
	var warns []string
	warn := func(format string, a ...any) { warns = append(warns, fmt.Sprintf(format, a...)) }
	dir, err := cwdpath.Resolve(root, Dir(root))
	if err != nil {
		warn("playbooks: %v", err)
		return nil, warns
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			warn("playbooks: %v", err)
		}
		return nil, warns
	}
	var user, pstack []*Playbook
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		resolved, err := cwdpath.Resolve(root, path)
		if err != nil {
			warn("playbooks: skipping %s: %v", path, err)
			continue
		}
		fi, err := os.Stat(resolved)
		if err != nil {
			warn("playbooks: skipping %s: %v", path, err)
			continue
		}
		if !fi.IsDir() {
			if strings.HasSuffix(e.Name(), ".md") && !strings.EqualFold(e.Name(), "README.md") {
				if p, err := load(root, path, strings.TrimSuffix(e.Name(), ".md")); err != nil {
					warn("playbooks: skipping %s: %v", path, err)
				} else {
					user = append(user, p)
				}
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(resolved, "PLAYBOOK.md")); err == nil {
			if p, err := load(root, filepath.Join(path, "PLAYBOOK.md"), e.Name()); err != nil {
				warn("playbooks: skipping %s: %v", filepath.Join(path, "PLAYBOOK.md"), err)
			} else {
				user = append(user, p)
			}
			continue
		}
		if pbDir, skillsDir, ok := pstackLayout(resolved); ok {
			ps, w := discoverPstack(root, pbDir, skillsDir)
			pstack = append(pstack, ps...)
			warns = append(warns, w...)
			continue
		}
		warn("playbooks: skipping %s: unknown layout (no PLAYBOOK.md and not a pstack checkout)", path)
	}
	var out []*Playbook
	seen := map[string]*Playbook{}
	for _, p := range append(user, pstack...) {
		if prev, ok := seen[p.Name]; ok {
			warn("playbooks: %s (%s) is shadowed by %s (%s)", p.Path, p.Format, prev.Path, prev.Format)
			continue
		}
		seen[p.Name] = p
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warns
}

func load(root, path, name string) (*Playbook, error) {
	data, err := readFile(root, path)
	if err != nil {
		return nil, err
	}
	p, err := Parse(path, name, data)
	if err != nil {
		return nil, err
	}
	p.SkillsDir = filepath.Join(root, "skills")
	return p, nil
}

func readFile(root, path string) ([]byte, error) {
	resolved, err := cwdpath.Resolve(root, path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if fi.Size() > MaxFileBytes {
		return nil, fmt.Errorf("over the %d byte limit", MaxFileBytes)
	}
	return os.ReadFile(resolved)
}

// Get returns the playbook named name.
func Get(pbs []*Playbook, name string) (*Playbook, bool) {
	for _, p := range pbs {
		if p.Name == name {
			return p, true
		}
	}
	return nil, false
}

// Names lists the playbooks' names.
func Names(pbs []*Playbook) []string {
	out := make([]string, len(pbs))
	for i, p := range pbs {
		out[i] = p.Name
	}
	return out
}

// Skill is one skill a playbook auto-loads.
type Skill struct {
	Name, Path, Body string
	raw              []byte
}

// LoadSkills reads the skills p names from p.SkillsDir (only those). A
// missing or unreadable skill, or one outside root, is a warning.
func (p *Playbook) LoadSkills(root string) ([]Skill, []string) {
	var out []Skill
	var warns []string
	for _, name := range p.Skills {
		path := filepath.Join(p.SkillsDir, name, "SKILL.md")
		data, err := readFile(root, path)
		if err != nil {
			if os.IsNotExist(err) {
				warns = append(warns, fmt.Sprintf("playbook %s: skill %q not found (%s)", p.Name, name, path))
			} else {
				warns = append(warns, fmt.Sprintf("playbook %s: skill %q: %v", p.Name, name, err))
			}
			continue
		}
		body := string(data)
		if _, b, err := splitFrontmatter(data); err == nil {
			body = string(b)
		}
		body = strings.TrimSpace(body)
		if len(body) > MaxSkillBytes {
			body = body[:MaxSkillBytes] + "\n[truncated]"
			warns = append(warns, fmt.Sprintf("playbook %s: skill %q truncated to %d bytes", p.Name, name, MaxSkillBytes))
		}
		out = append(out, Skill{Name: name, Path: path, Body: body, raw: data})
	}
	return out, warns
}

// Hash is the content hash of the playbook and the skills it loaded; a
// resume refuses to continue when it changed.
func Hash(p *Playbook, skills []Skill) string {
	h := sha256.New()
	h.Write(p.raw)
	for _, s := range skills {
		fmt.Fprintf(h, "\x00skill %s\x00", s.Name)
		h.Write(s.raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Section renders p and its skills for the system prompt.
func Section(p *Playbook, skills []Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n## Playbook: %s\n\n%s\n\nWork through this checklist in order. Don't skip a step; if one doesn't apply, say why.\n", p.Name, p.Intent)
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "- [ ] %d. %s\n", i+1, s)
	}
	if p.Done != "" {
		fmt.Fprintf(&b, "\nDone when:\n%s\n", p.Done)
	}
	for _, s := range skills {
		fmt.Fprintf(&b, "\n### Skill %s (loaded by the playbook)\n\n%s\n", s.Name, s.Body)
	}
	return b.String()
}
