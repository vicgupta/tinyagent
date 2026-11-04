// Package dotenv loads KEY=VALUE pairs from a project's ./.env file into the
// process environment. Variables already set in the environment win, so a
// .env file only fills gaps. Values are never logged.
package dotenv

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the only file read: ./.env relative to the working directory.
const FileName = ".env"

// Aliases maps a .env key that has no environment meaning of its own onto
// the variable tinyagent reads, matched case-insensitively. These mirror
// tinyagent.yaml keys that have an environment equivalent. A model=provider/name line sets
// AGENT_MODEL. An AGENT_MODEL line in the same file wins over the alias, and
// a real AGENT_MODEL in the environment wins over both.
var Aliases = map[string]string{"model": "AGENT_MODEL"}

// Load reads dir/.env and sets every variable not already present in the
// environment. A missing file is not an error. It returns the names (never
// the values) of the variables it set.
func Load(dir string) ([]string, error) {
	return load(filepath.Join(dir, FileName), os.LookupEnv, os.Setenv)
}

func load(path string, lookup func(string) (string, bool), set func(string, string) error) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	aliased := map[string]string{}
	var aliasOrder []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		k, v, ok, err := parseLine(sc.Text())
		if err != nil {
			return names, fmt.Errorf("%s:%d: %w", FileName, n, err)
		}
		if !ok {
			continue
		}
		if target, isAlias := Aliases[strings.ToLower(k)]; isAlias {
			if _, seen := aliased[target]; !seen {
				aliasOrder = append(aliasOrder, target)
			}
			aliased[target] = v
			continue
		}
		if _, exists := lookup(k); exists {
			continue
		}
		if err := set(k, v); err != nil {
			return names, fmt.Errorf("%s:%d: setting %s: %w", FileName, n, k, err)
		}
		names = append(names, k)
	}
	if err := sc.Err(); err != nil {
		return names, err
	}
	for _, k := range aliasOrder {
		if _, exists := lookup(k); exists {
			continue
		}
		if err := set(k, aliased[k]); err != nil {
			return names, fmt.Errorf("%s: setting %s: %w", FileName, k, err)
		}
		names = append(names, k)
	}
	return names, nil
}

// parseLine parses one line. Blank lines and # comments return ok=false.
// Supports an optional "export " prefix, single quotes (literal), double
// quotes (with \n, \t, \" and \\ escapes) and trailing " # comments" on
// unquoted values.
func parseLine(line string) (key, val string, ok bool, err error) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false, errors.New("expected KEY=VALUE")
	}
	key = strings.TrimSpace(line[:i])
	if !validKey(key) {
		return "", "", false, fmt.Errorf("invalid variable name %q", key)
	}
	raw := strings.TrimSpace(line[i+1:])
	switch {
	case strings.HasPrefix(raw, "'"):
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", "", false, fmt.Errorf("unterminated quote for %s", key)
		}
		return key, raw[1 : 1+end], true, nil
	case strings.HasPrefix(raw, `"`):
		var b strings.Builder
		for j := 1; j < len(raw); j++ {
			c := raw[j]
			if c == '"' {
				return key, b.String(), true, nil
			}
			if c == '\\' && j+1 < len(raw) {
				j++
				switch raw[j] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(raw[j])
				}
				continue
			}
			b.WriteByte(c)
		}
		return "", "", false, fmt.Errorf("unterminated quote for %s", key)
	}
	if j := strings.Index(raw, " #"); j >= 0 {
		raw = strings.TrimSpace(raw[:j])
	}
	return key, raw, true, nil
}

func validKey(k string) bool {
	for i, c := range k {
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return k != ""
}
