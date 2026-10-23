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
	dir, err := Resolve(root, path)
	if err != nil {
		return "", err
	}
	if pattern == "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		var lines []string
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			lines = append(lines, name)
		}
		return strings.Join(lines, "\n"), nil
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return "", fmt.Errorf("bad pattern %q: %w", pattern, err)
	}

	var matches []string
	matchPath := strings.Contains(pattern, "/")
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if p != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		subject := d.Name()
		if matchPath {
			subject = rel(dir, p)
		}
		if ok, _ := filepath.Match(pattern, subject); ok {
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
	return out, nil
}
