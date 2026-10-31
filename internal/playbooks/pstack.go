package playbooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/cwdpath"
)

// pstack layout: a checkout of github.com/cursor/plugins (pstack/ inside
// it) or of pstack itself, with playbooks in skills/poteto-mode/playbooks
// and skills in skills/.
var pstackRoots = []string{".", "pstack"}

func pstackLayout(dir string) (pbDir, skillsDir string, ok bool) {
	for _, r := range pstackRoots {
		base := filepath.Join(dir, r)
		pb := filepath.Join(base, "skills", "poteto-mode", "playbooks")
		if fi, err := os.Stat(pb); err == nil && fi.IsDir() {
			return pb, filepath.Join(base, "skills"), true
		}
	}
	return "", "", false
}

func discoverPstack(root, pbDir, skillsDir string) ([]*Playbook, []string) {
	var warns []string
	entries, err := os.ReadDir(pbDir)
	if err != nil {
		return nil, []string{fmt.Sprintf("playbooks: pstack %s: %v", pbDir, err)}
	}
	var out []*Playbook
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(pbDir, e.Name())
		data, err := readFile(root, path)
		if err == nil {
			var p *Playbook
			if p, err = ParsePstack(path, strings.TrimSuffix(e.Name(), ".md"), data, func(name string) bool {
				_, err := cwdpath.Resolve(root, filepath.Join(skillsDir, name, "SKILL.md"))
				if err != nil {
					return false
				}
				_, err = os.Stat(filepath.Join(skillsDir, name, "SKILL.md"))
				return err == nil
			}); err == nil {
				p.SkillsDir = skillsDir
				out = append(out, p)
				continue
			}
		}
		warns = append(warns, fmt.Sprintf("playbooks: skipping pstack playbook %s: %v", path, err))
	}
	return out, warns
}

var (
	reNumbered = regexp.MustCompile(`^(\d+)[.)]\s+(.+)$`)
	reRef      = regexp.MustCompile("`([a-z0-9][a-z0-9-]*)`|\\*\\*([a-z0-9][a-z0-9-]*)\\*\\*")
	reLabel    = regexp.MustCompile(`^\*\*(Reply|Done[^*]*):\*\*\s*(.*)$`)
)

// ParsePstack reads a pstack playbook as-is: the title heading and opening
// section are its intent, its top-level numbered list is the steps, and a
// "**Reply:**" (or "**Done...:**") paragraph is the done check. Skills are
// the backticked or bold names it mentions that exist in the checkout's
// skills/ (isSkill reports that). A file without a title or numbered steps
// is an error, so discovery skips it with a warning.
func ParsePstack(path, name string, data []byte, isSkill func(string) bool) (*Playbook, error) {
	if !reName.MatchString(name) {
		return nil, fmt.Errorf("file name %q is not a playbook name", name)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) {
		return nil, errors.New("empty file")
	}
	m := reHeading.FindStringSubmatch(lines[i])
	if m == nil || strings.TrimSpace(m[2]) == "" {
		return nil, errors.New("unknown layout: no title heading on the first line")
	}
	title := strings.TrimSpace(m[2])
	var opening []string
	j := i + 1
	for ; j < len(lines); j++ {
		l := strings.TrimSpace(lines[j])
		if reNumbered.MatchString(l) || reHeading.MatchString(lines[j]) {
			break
		}
		if l != "" {
			opening = append(opening, l)
		}
	}
	intent := title
	if len(opening) > 0 {
		intent += ". " + strings.Join(opening, " ")
	}
	if len(intent) > 600 {
		intent = intent[:600] + "…"
	}
	p := &Playbook{Name: name, Intent: intent, Path: path, Format: FormatPstack, raw: data}
	for _, line := range lines[j:] {
		t := strings.TrimSpace(line)
		if mm := reNumbered.FindStringSubmatch(line); mm != nil {
			p.Steps = append(p.Steps, strings.TrimSpace(mm[2]))
		} else if lm := reLabel.FindStringSubmatch(t); lm != nil {
			p.Done = strings.TrimSpace(lm[2])
		} else if len(p.Steps) > 0 && t != "" && p.Done == "" && (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) {
			p.Steps[len(p.Steps)-1] += " " + t
		}
	}
	if len(p.Steps) == 0 {
		return nil, errors.New("unknown layout: no numbered steps")
	}
	seen := map[string]bool{}
	for _, mm := range reRef.FindAllStringSubmatch(string(data), -1) {
		n := mm[1] + mm[2]
		if !seen[n] && n != name && isSkill != nil && isSkill(n) {
			seen[n] = true
			p.Skills = append(p.Skills, n)
		}
	}
	sort.Strings(p.Skills)
	return p, nil
}
