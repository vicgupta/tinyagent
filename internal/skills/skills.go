// Package skills discovers Agent Skills (folders holding a SKILL.md) in the
// project's ./skills directory (relative to the run's cwd) and in plugins,
// renders the catalog added to the system prompt, and loads a skill's
// instructions or one of its bundled files for the skill tool.
package skills

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
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/vicgupta/tinyagent/internal/model"
)

// ToolName is the tool that loads a skill.
const ToolName = "skill"

// Bounds.
const (
	// MaxSkillFileBytes caps how much of a SKILL.md or bundled file is read.
	MaxSkillFileBytes = 1 << 20
	// MaxCatalogChars caps the catalog section in the system prompt.
	MaxCatalogChars = 8000
	// maxDescriptionChars is what a description is shortened to when the
	// full catalog does not fit.
	maxDescriptionChars = 200
	// MaxListedFiles caps the bundled file list in a skill's result.
	MaxListedFiles = 100
	// maxWalk bounds how many directory entries are visited when listing.
	maxWalk = 10000
)

// Scopes.
const (
	ScopeProject = "project"
	ScopePlugin  = "plugin"
)

// ContentPrefix starts every successful instructions result.
const ContentPrefix = "<skill_content name=\""

// Skill is one discovered skill.
type Skill struct {
	Name        string
	Description string
	Scope       string // ScopeProject or ScopePlugin
	Plugin      string // the plugin's name, for ScopePlugin
	Dir         string // absolute skill directory
	Path        string // absolute SKILL.md path
}

// Set is the skills discovered for one run, sorted by name.
type Set struct {
	Skills []Skill
	// Omitted counts valid skills left out because the catalog was full.
	Omitted int
}

// Len returns the number of skills; a nil Set has none.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Skills)
}

// Get returns the skill named name.
func (s *Set) Get(name string) (Skill, bool) {
	if s != nil {
		for _, k := range s.Skills {
			if k.Name == name {
				return k, true
			}
		}
	}
	return Skill{}, false
}

// Names returns the skill names in catalog order.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.Skills))
	for i, k := range s.Skills {
		out[i] = k.Name
	}
	return out
}

// Dir is the project skills directory, relative to the run's cwd.
const Dir = "skills"

// ProjectDirs returns the directories scanned for project skills: only
// ./skills in root (the run's --cwd). There is no walk up to the
// repository root and no home-folder scope (#359).
func ProjectDirs(root string) []string {
	return []string{filepath.Join(root, Dir)}
}

// legacyDirs are earlier or other tools' skill locations, relative to the
// cwd. They are never scanned; finding one only produces a hint.
var legacyDirs = []string{filepath.Join(".agents", "skills"), filepath.Join(".claude", "skills")}

// Discover scans ./skills in root. Problems never fail discovery: each is
// returned as a warning and the affected skill is skipped (or loaded, for
// cosmetic name problems).
func Discover(root string) (*Set, []string) {
	return DiscoverWith(root, nil)
}

// PluginSource is one plugin's skills/ directory. Root is the plugin's
// filesystem-resolved root: the skills directory, each skill directory and
// its SKILL.md must resolve inside it.
type PluginSource struct {
	Plugin string
	Root   string
	Dir    string
}

// DiscoverWith is Discover plus plugin skills, which rank below project
// skills, in the order given.
func DiscoverWith(root string, plugins []PluginSource) (*Set, []string) {
	var warnings []string
	warn := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }
	type scan struct{ dir, scope, plugin, root string }
	var scans []scan
	for _, d := range ProjectDirs(root) {
		scans = append(scans, scan{dir: d, scope: ScopeProject})
	}
	for _, d := range legacyDirs {
		if fi, err := os.Stat(filepath.Join(root, d)); err == nil && fi.IsDir() {
			warn("skills: %s is not scanned; move or symlink its skills into ./%s", filepath.ToSlash(d), Dir)
		}
	}
	for _, p := range plugins {
		scans = append(scans, scan{dir: p.Dir, scope: ScopePlugin, plugin: p.Plugin, root: p.Root})
	}
	byName := map[string]int{}
	var found []Skill
	seenDir := map[string]bool{}
	for _, sc := range scans {
		real, err := filepath.EvalSymlinks(sc.dir)
		if err != nil {
			continue // missing (or broken) scope directory
		}
		if sc.scope == ScopePlugin {
			if !within(sc.root, real) {
				warn("skills: plugin %s: %s resolves outside the plugin root; its skills are skipped", sc.plugin, sc.dir)
				continue
			}
			if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
				warn("skills: plugin %s: %s is not a directory; its skills are skipped", sc.plugin, sc.dir)
				continue
			}
		}
		if seenDir[real] {
			continue // e.g. a plugin's skills directory is ./skills
		}
		seenDir[real] = true
		entries, err := os.ReadDir(sc.dir)
		if err != nil {
			warn("skills: reading %s: %v", sc.dir, err)
			continue
		}
		for _, e := range entries {
			dir := filepath.Join(sc.dir, e.Name())
			fi, err := os.Stat(dir) // follows symlinked skill directories
			if err != nil || !fi.IsDir() {
				continue
			}
			path := filepath.Join(dir, "SKILL.md")
			if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if sc.scope == ScopePlugin {
				rdir, err1 := filepath.EvalSymlinks(dir)
				rpath, err2 := filepath.EvalSymlinks(path)
				if err1 != nil || err2 != nil || !within(sc.root, rdir) || !within(sc.root, rpath) {
					warn("skills: plugin %s: skipping %s: it resolves outside the plugin root", sc.plugin, dir)
					continue
				}
				dir, path = rdir, rpath
			}
			k, err := parse(path, e.Name())
			if err != nil {
				warn("skills: skipping %s: %v", path, err)
				continue
			}
			for _, p := range nameProblems(k.Name, e.Name()) {
				warn("skills: %s: %s (loaded anyway)", path, p)
			}
			k.Scope, k.Dir, k.Path, k.Plugin = sc.scope, dir, path, sc.plugin
			if i, ok := byName[k.Name]; ok {
				warn("skills: skill %q at %s is shadowed by %s", k.Name, path, found[i].Path)
				continue
			}
			byName[k.Name] = len(found)
			found = append(found, k)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	set := &Set{Skills: found}
	if n := set.fit(); n > 0 {
		warn("skills: catalog exceeds %d characters; %d skill(s) omitted", MaxCatalogChars, n)
	}
	return set, warnings
}

// fit drops skills from the end until the catalog fits, returning how many
// were dropped. Descriptions are shortened first (see Catalog).
func (s *Set) fit() int {
	n := 0
	for len(s.Skills) > 0 && len(s.Catalog()) > MaxCatalogChars {
		s.Skills = s.Skills[:len(s.Skills)-1]
		n++
	}
	s.Omitted = n
	return n
}

var nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func nameProblems(name, dir string) []string {
	var out []string
	if len(name) > 64 {
		out = append(out, fmt.Sprintf("name %q is longer than 64 characters", name))
	}
	if !nameRE.MatchString(name) {
		out = append(out, fmt.Sprintf("name %q should use only lowercase letters, digits, and single hyphens", name))
	}
	if name != dir {
		out = append(out, fmt.Sprintf("name %q does not match its directory %q", name, dir))
	}
	return out
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parse reads a SKILL.md's frontmatter. dirName is the fallback name.
func parse(path, dirName string) (Skill, error) {
	b, err := readCapped(path)
	if err != nil {
		return Skill{}, err
	}
	fm, _, err := split(b)
	if err != nil {
		return Skill{}, err
	}
	var f frontmatter
	if err := yaml.Unmarshal(fm, &f); err != nil {
		return Skill{}, fmt.Errorf("invalid YAML frontmatter: %v", err)
	}
	f.Name, f.Description = strings.TrimSpace(f.Name), strings.TrimSpace(f.Description)
	if f.Description == "" {
		return Skill{}, errors.New("missing or empty description")
	}
	if len(f.Description) > 1024 {
		f.Description = cut(f.Description, 1024)
	}
	if f.Name == "" {
		f.Name = dirName
	}
	if strings.ContainsAny(f.Name, "\r\n") {
		return Skill{}, errors.New("name contains a line break")
	}
	return Skill{Name: f.Name, Description: f.Description}, nil
}

// split separates the frontmatter (between a leading --- line and the next
// --- line) from the body.
func split(b []byte) (fm, body []byte, err error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	first, rest, ok := bytes.Cut(b, []byte("\n"))
	if !ok || strings.TrimSpace(string(first)) != "---" {
		return nil, nil, errors.New("missing YAML frontmatter (a leading --- line)")
	}
	for off := 0; off <= len(rest); {
		line, next, more := bytes.Cut(rest[off:], []byte("\n"))
		if strings.TrimSpace(string(line)) == "---" {
			body := []byte(nil)
			if more {
				body = next
			}
			return rest[:off], body, nil
		}
		if !more {
			break
		}
		off += len(line) + 1
	}
	return nil, nil, errors.New("unterminated YAML frontmatter (no closing --- line)")
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSkillFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSkillFileBytes {
		return nil, fmt.Errorf("file exceeds the %d byte limit", MaxSkillFileBytes)
	}
	return b, nil
}

// cut shortens s to at most n bytes on a rune boundary, adding "…".
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n]) + "…"
}

const catalogIntro = "\n\n## Skills\n\n" +
	"These skills hold instructions for specific tasks. When a task matches a skill's description, " +
	"call the skill tool with its name to load the instructions before you start, then follow them. " +
	"Load a bundled file with the skill tool's file argument; run bundled scripts with bash.\n\n"

// Catalog returns the system prompt section listing every skill, or "" when
// there are none. Descriptions are shortened when the full list would exceed
// MaxCatalogChars.
func (s *Set) Catalog() string {
	if s.Len() == 0 {
		return ""
	}
	render := func(short bool) string {
		var sb strings.Builder
		sb.WriteString(catalogIntro)
		for _, k := range s.Skills {
			d := strings.Join(strings.Fields(k.Description), " ")
			if short {
				d = cut(d, maxDescriptionChars)
			}
			fmt.Fprintf(&sb, "- %s: %s\n", k.Name, d)
		}
		return strings.TrimRight(sb.String(), "\n")
	}
	if c := render(false); len(c) <= MaxCatalogChars {
		return c
	}
	return render(true)
}

// Spec returns the skill tool definition, its name constrained to the
// discovered names.
func (s *Set) Spec() model.ToolSpec {
	return model.ToolSpec{
		Type: "function",
		Function: model.FunctionSpec{
			Name: ToolName,
			Description: "Load a skill listed in the system prompt. Without file, returns the skill's full instructions, " +
				"its directory, and its bundled files. With file, returns one bundled text file.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Skill name", "enum": s.Names()},
					"file": map[string]any{"type": "string", "description": "Optional path of a bundled file, relative to the skill directory (e.g. references/guide.md)"},
				},
				"required": []string{"name"},
			},
		},
	}
}

// Load returns the instructions of the skill named name, or, when file is
// set, the text of that file inside the skill directory.
func (s *Set) Load(name, file string) (string, error) {
	k, ok := s.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q; available: %s", name, strings.Join(s.Names(), ", "))
	}
	if file != "" {
		return readBundled(k, file)
	}
	b, err := readCapped(k.Path)
	if err != nil {
		return "", err
	}
	_, body, err := split(b)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s%s\">\n", ContentPrefix, k.Name)
	sb.WriteString(strings.TrimSpace(string(body)))
	fmt.Fprintf(&sb, "\n\nSkill directory: %s\nRelative paths in these instructions are relative to the skill directory.\n", k.Dir)
	files, more := listFiles(k.Dir)
	if len(files) > 0 {
		sb.WriteString("Bundled files (load one with the skill tool's file argument):\n")
		for _, f := range files {
			fmt.Fprintf(&sb, "- %s\n", f)
		}
		if more {
			fmt.Fprintf(&sb, "- ... (more files not listed; at most %d are shown)\n", MaxListedFiles)
		}
	}
	sb.WriteString("</skill_content>")
	return sb.String(), nil
}

// readBundled reads file inside k.Dir, refusing absolute paths, ".."
// escapes, and symlinks that resolve outside the directory.
func readBundled(k Skill, file string) (string, error) {
	if filepath.IsAbs(file) || strings.HasPrefix(file, "/") || strings.HasPrefix(file, `\`) || filepath.VolumeName(file) != "" {
		return "", fmt.Errorf("file %q must be relative to the skill directory", file)
	}
	clean := filepath.Clean(filepath.FromSlash(file))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file %q is outside the skill directory", file)
	}
	dir, err := filepath.EvalSymlinks(k.Dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(dir, clean))
	if err != nil {
		return "", fmt.Errorf("file %q: %w", file, errors.Unwrap(err))
	}
	if !within(dir, real) {
		return "", fmt.Errorf("file %q resolves outside the skill directory", file)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("file %q is not a regular file", file)
	}
	b, err := readCapped(real)
	if err != nil {
		return "", fmt.Errorf("file %q: %v", file, err)
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return "", fmt.Errorf("file %q looks like a binary file (%d bytes)", file, len(b))
	}
	return string(b), nil
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// listFiles returns up to MaxListedFiles bundled files (not SKILL.md),
// slash-separated and relative to dir, and whether more exist. Symlinked
// subdirectories are not descended.
func listFiles(dir string) ([]string, bool) {
	var out []string
	more, visited := false, 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if visited++; visited > maxWalk {
			more = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != dir && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "SKILL.md" {
			return nil
		}
		if len(out) == MaxListedFiles {
			more = true
			return filepath.SkipAll
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, more
}
