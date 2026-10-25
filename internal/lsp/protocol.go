package lsp

import (
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Position is a zero-based LSP position; Character counts UTF-16 code units.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is an LSP range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Severities of a Diagnostic.
const (
	SeverityError   = 1
	SeverityWarning = 2
)

// Diagnostic is one published diagnostic.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// IsError reports whether d is an error; a missing severity counts as one.
func (d Diagnostic) IsError() bool { return d.Severity == 0 || d.Severity == SeverityError }

// Location is a resolved definition or reference: an absolute path and a
// zero-based UTF-16 range.
type Location struct {
	Path  string
	Range Range
}

// PathToURI returns the file URI for an absolute path.
func PathToURI(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// URIToPath returns the absolute path of a file URI.
func URIToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", errors.New("not a file URI: " + uri)
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		if u.Host != "" {
			p = "//" + u.Host + p
		} else if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
	}
	return filepath.Clean(filepath.FromSlash(p)), nil
}

// RuneToUTF16 converts a zero-based rune column in line to UTF-16 units.
// A column past the end is clamped to the line length.
func RuneToUTF16(line string, col int) int {
	n := 0
	for i, r := range []rune(line) {
		if i >= col {
			break
		}
		n += utf16.RuneLen(r)
	}
	return n
}

// UTF16ToRune converts a zero-based UTF-16 column in line to runes.
func UTF16ToRune(line string, units int) int {
	n, cols := 0, 0
	for _, r := range line {
		if n >= units {
			break
		}
		if r == utf8.RuneError {
			n++
		} else {
			n += utf16.RuneLen(r)
		}
		cols++
	}
	return cols
}

// languageIDs maps file extensions to LSP language identifiers; other
// extensions use the extension without the dot.
var languageIDs = map[string]string{
	".go": "go", ".py": "python", ".rs": "rust", ".ts": "typescript", ".tsx": "typescriptreact",
	".js": "javascript", ".jsx": "javascriptreact", ".mjs": "javascript", ".cjs": "javascript",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hh": "cpp",
	".java": "java", ".kt": "kotlin", ".rb": "ruby", ".cs": "csharp", ".sh": "shellscript",
	".md": "markdown", ".yaml": "yaml", ".yml": "yaml", ".json": "json", ".zig": "zig", ".lua": "lua",
	".swift": "swift", ".php": "php", ".scala": "scala", ".ex": "elixir", ".exs": "elixir",
}

func languageID(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if id, ok := languageIDs[ext]; ok {
		return id
	}
	return strings.TrimPrefix(ext, ".")
}

// parseLocations decodes a definition or references result: null, a
// Location, a Location array or a LocationLink array.
func parseLocations(raw json.RawMessage) ([]Location, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, nil
	}
	type loc struct {
		URI                  string `json:"uri"`
		Range                *Range `json:"range"`
		TargetURI            string `json:"targetUri"`
		TargetSelectionRange *Range `json:"targetSelectionRange"`
		TargetRange          *Range `json:"targetRange"`
	}
	var items []loc
	if strings.HasPrefix(s, "{") {
		var one loc
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		items = []loc{one}
	} else if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	var out []Location
	for _, it := range items {
		uri, rng := it.URI, it.Range
		if it.TargetURI != "" {
			uri, rng = it.TargetURI, it.TargetSelectionRange
			if rng == nil {
				rng = it.TargetRange
			}
		}
		if uri == "" || rng == nil {
			continue
		}
		p, err := URIToPath(uri)
		if err != nil {
			continue
		}
		out = append(out, Location{Path: p, Range: *rng})
	}
	return out, nil
}
