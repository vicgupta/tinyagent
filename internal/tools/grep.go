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
	maxGrepMatches            = 500
	maxGrepFileSize           = 5 << 20
	maxGrepContextLines       = 20
	maxGrepContextOutputLines = 500
)

// Grep returns lines matching pattern as "file:line: content". Binary and
// very large files are skipped. A non-empty include glob limits the search to
// matching files, using the same rules as ListFiles: a glob without "/"
// matches base names, a glob with "/" matches paths relative to the searched
// directory. When path is a single file, include is ignored. ignoreCase enables
// case-insensitive matching of file contents only. fixedStrings treats pattern
// as literal text instead of a Go regular expression.
func Grep(root, pattern, path, include string, ignoreCase, fixedStrings bool) (string, error) {
	return GrepWithContext(root, pattern, path, include, ignoreCase, fixedStrings, 0)
}

// GrepWithContext returns matches with up to contextLines surrounding lines.
func GrepWithContext(root, pattern, path, include string, ignoreCase, fixedStrings bool, contextLines int) (string, error) {
	return grepWithContext(root, pattern, path, include, ignoreCase, fixedStrings, contextLines, "", nil)
}

// grepWithContext is GrepWithContext that also skips the directory skip (an
// absolute path; "" skips nothing extra) unless the search starts inside it,
// and never opens or reports paths fa denies for reading, ending with a count
// of the ones it hid.
func grepWithContext(root, pattern, path, include string, ignoreCase, fixedStrings bool, contextLines int, skip string, fa *FileAccess) (string, error) {
	if fixedStrings {
		pattern = regexp.QuoteMeta(pattern)
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
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
	if err := fa.CheckRead(start, displayPath(path)); err != nil {
		return "", err
	}
	hidden := 0

	var out []string
	matches, sourceLines := 0, 0
	contextTruncated := false
	err = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != start && (skipDir(d.Name()) || p == skip) {
				return filepath.SkipDir
			}
			if p != start && fa.Hidden(p) {
				hidden++
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
		if p != start && fa.Hidden(p) {
			hidden++
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxGrepFileSize {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || isBinary(data) {
			return nil
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), maxGrepFileSize)
		if contextLines == 0 {
			for n := 1; sc.Scan(); n++ {
				if line := sc.Text(); re.MatchString(line) {
					out = append(out, fmt.Sprintf("%s:%d: %s", rel(root, p), n, line))
					matches++
					if matches >= maxGrepMatches {
						return fs.SkipAll
					}
				}
			}
			return nil
		}
		// Positive context mode buffers one bounded file (files are capped at
		// maxGrepFileSize), then emits merged ranges while enforcing the output
		// line budget. The buffer is discarded before the next file.
		var lines []string
		var hit []bool
		for sc.Scan() {
			line := sc.Text()
			lines = append(lines, line)
			matched := re.MatchString(line)
			hit = append(hit, matched)
			if matched {
				matches++
			}
		}
		for i := 0; i < len(lines); {
			if !hit[i] {
				i++
				continue
			}
			lo, hi := max(0, i-contextLines), min(len(lines)-1, i+contextLines)
			for j := i + 1; j < len(lines) && j <= hi+contextLines+1; j++ {
				if hit[j] {
					hi = min(len(lines)-1, j+contextLines)
				}
			}
			if len(out) > 0 {
				out = append(out, "--")
			}
			for j := lo; j <= hi; j++ {
				if sourceLines >= maxGrepContextOutputLines {
					contextTruncated = true
					return fs.SkipAll
				}
				marker := "-"
				if hit[j] {
					marker = ":"
				}
				out = append(out, fmt.Sprintf("%s%s%d%s %s", rel(root, p), marker, j+1, marker, lines[j]))
				sourceLines++
			}
			i = hi + 1
			if matches >= maxGrepMatches {
				contextTruncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return withHiddenNote("no matches", hidden), nil
	}
	res := strings.Join(out, "\n")
	if contextLines == 0 && matches >= maxGrepMatches {
		res += fmt.Sprintf("\n... (stopped at %d matches)", maxGrepMatches)
	}
	if contextLines > 0 && contextTruncated {
		res += "\n... (results/context may be incomplete: stopped at output limit)"
	}
	return withHiddenNote(res, hidden), nil
}
