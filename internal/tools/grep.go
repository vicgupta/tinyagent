package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxGrepMatches  = 500
	maxGrepFileSize = 5 << 20
)

// Grep returns lines matching pattern as "file:line: content". Binary and
// very large files are skipped.
func Grep(root, pattern, path string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("bad regexp: %w", err)
	}
	start, err := Resolve(root, path)
	if err != nil {
		return "", err
	}

	var out []string
	err = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != start && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxGrepFileSize {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return nil
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), maxGrepFileSize)
		for n := 1; sc.Scan(); n++ {
			if line := sc.Text(); re.MatchString(line) {
				out = append(out, fmt.Sprintf("%s:%d: %s", rel(root, p), n, line))
				if len(out) >= maxGrepMatches {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "no matches", nil
	}
	res := strings.Join(out, "\n")
	if len(out) >= maxGrepMatches {
		res += fmt.Sprintf("\n... (stopped at %d matches)", maxGrepMatches)
	}
	return res, nil
}
