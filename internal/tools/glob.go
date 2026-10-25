package tools

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxListEntries = 1000

// ListFiles lists the entries of a directory, or, with a pattern, walks it
// recursively and returns matching files. A pattern without "/" matches base
// names; a pattern with "/" matches paths relative to the listed directory.
func ListFiles(root, path, pattern string) (string, error) {
	return listFiles(root, path, pattern, "", nil)
}

// listFiles is ListFiles that also skips the directory skip (an absolute
// path; "" skips nothing extra) unless the walk starts inside it, and hides
// paths fa denies for reading, ending with a count of them.
func listFiles(root, path, pattern, skip string, fa *FileAccess) (string, error) {
	dir, err := Resolve(root, path)
	if err != nil {
		return "", err
	}
	if err := fa.CheckRead(dir, displayPath(path)); err != nil {
		return "", err
	}
	if pattern == "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		var lines []string
		hidden := 0
		for _, e := range entries {
			if fa.Hidden(filepath.Join(dir, e.Name())) {
				hidden++
				continue
			}
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			lines = append(lines, name)
		}
		return withHiddenNote(strings.Join(lines, "\n"), hidden), nil
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return "", fmt.Errorf("bad pattern %q: %w", pattern, err)
	}

	var matches []string
	hidden := 0
	matchPath := strings.Contains(pattern, "/")
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if p != dir && (skipDir(d.Name()) || p == skip) {
				return filepath.SkipDir
			}
			if p != dir && fa.Hidden(p) {
				hidden++
				return filepath.SkipDir
			}
			return nil
		}
		subject := d.Name()
		if matchPath {
			subject = rel(dir, p)
		}
		if ok, _ := filepath.Match(pattern, subject); ok {
			if fa.Hidden(p) {
				hidden++
				return nil
			}
			matches = append(matches, rel(root, p))
			if len(matches) >= maxListEntries {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	if len(matches) >= maxListEntries {
		out += fmt.Sprintf("\n... (stopped at %d results)", maxListEntries)
	}
	if out == "" {
		out = "no matches"
	}
	return withHiddenNote(out, hidden), nil
}

// displayPath is the path as the model named it, "." when omitted.
func displayPath(p string) string {
	if p == "" {
		return "."
	}
	return p
}
