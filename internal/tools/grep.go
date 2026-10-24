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

// Grep returns lines matching pattern as "file:line: content". The pattern is a
// Go regular expression unless fixedStrings is true, which matches literal text.
// Binary and very large files are skipped. A non-empty include glob limits the search to
// matching files, using the same rules as ListFiles: a glob without "/"
// matches base names, a glob with "/" matches paths relative to the searched
// directory. When path is a single file, include is ignored.
func Grep(root, pattern, path, include string, fixedStrings bool) (string, error) {
	if fixedStrings {
		pattern = regexp.QuoteMeta(pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("bad regexp: %w", err)
	}
	if include != "" {
		if _, err := filepath.Match(include, ""); err != nil {
			return "", fmt.Errorf("bad include pattern %q: %w", include, err)
		}
	}
	matchPath := strings.Contains(include, "/")
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
		if include != "" && p != start {
			subject := d.Name()
			if matchPath {
				subject = rel(start, p)
			}
			if ok, _ := filepath.Match(include, subject); !ok {
				return nil
			}
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
