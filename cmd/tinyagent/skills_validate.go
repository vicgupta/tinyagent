package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/harness"
)

// runSkillsValidate handles --skills-validate (tinyagent skills validate):
// it checks skill-workflow skills and workflow files with the same parser
// and rules --workflow uses at load time, without calling a model. Each path
// is a skill file (.md), a skills directory or a workflow file (.yaml/.yml).
// It exits 0 when everything is valid, 1 when a skill or workflow is
// invalid, and 2 when a path doesn't exist or isn't one of those kinds.
func runSkillsValidate(paths []string, stdout, stderr io.Writer) int {
	code := 0
	fail := func(c int, err error) {
		fmt.Fprintln(stderr, "error:", err)
		if c > code {
			code = c
		}
	}
	report := func(s *harness.Skill) {
		fmt.Fprintf(stdout, "ok %s\n", s.Name)
		if s.SuccessCheck == "" {
			fmt.Fprintf(stderr, "warning: skill %s: no success check: needs a check: command when used as a stage\n", s.Name)
		}
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			fail(2, err)
			continue
		}
		ext := strings.ToLower(filepath.Ext(p))
		switch {
		case fi.IsDir():
			skills, err := harness.LoadSkills(p)
			if err != nil {
				fail(1, err)
				continue
			}
			if len(skills) == 0 {
				fail(1, fmt.Errorf("%s: no skills found (<name>.md or <name>/SKILL.md)", p))
				continue
			}
			for _, s := range sortedSkills(skills) {
				report(s)
			}
		case ext == ".md":
			s, err := harness.LoadSkillFile(p)
			if err != nil {
				fail(1, err)
				continue
			}
			report(s)
		case ext == ".yaml" || ext == ".yml":
			w, err := harness.Load(p)
			if err != nil {
				fail(1, err)
				continue
			}
			// Load already requires a check for every stage, so a skill
			// without its own success check is fine here.
			for _, s := range sortedSkills(w.Skills) {
				fmt.Fprintf(stdout, "ok %s\n", s.Name)
			}
			fmt.Fprintf(stdout, "ok workflow file %s (%d workflow(s), %d skill(s))\n", p, len(w.Pipelines), len(w.Skills))
		default:
			fail(2, fmt.Errorf("%s: not a skill file (.md), skills directory or workflow file (.yaml)", p))
		}
	}
	return code
}

func sortedSkills(m map[string]*harness.Skill) []*harness.Skill {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*harness.Skill, 0, len(names))
	for _, n := range names {
		out = append(out, m[n])
	}
	return out
}
