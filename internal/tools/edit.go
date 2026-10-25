package tools

import (
	"fmt"
	"os"
	"strings"
)

// EditFile replaces the single occurrence of oldStr with newStr, or every
// occurrence if replaceAll is set, and returns the number of replacements.
func EditFile(root, path, oldStr, newStr string, replaceAll bool) (int, error) {
	return editFile(root, path, oldStr, newStr, replaceAll, nil)
}

// editFile is EditFile that refuses paths the scope denies for writing.
func editFile(root, path, oldStr, newStr string, replaceAll bool, sc *scope) (int, error) {
	if oldStr == "" {
		return 0, fmt.Errorf("old_str must not be empty")
	}
	full, err := sc.resolve(root, path)
	if err != nil {
		return 0, err
	}
	if err := sc.checkWrite(full, path); err != nil {
		return 0, err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return 0, err
	}
	content := string(b)
	n := strings.Count(content, oldStr)
	switch {
	case n == 0:
		return 0, fmt.Errorf("old_str not found in %s", path)
	case n > 1 && !replaceAll:
		return 0, fmt.Errorf("old_str occurs %d times in %s; add surrounding context to make it unique", n, path)
	}
	fi, err := os.Stat(full)
	if err != nil {
		return 0, err
	}
	return n, os.WriteFile(full, []byte(strings.ReplaceAll(content, oldStr, newStr)), fi.Mode().Perm())
}
