package tools

import (
	"fmt"
	"os"
	"strings"
)

// EditFile replaces the single occurrence of oldStr with newStr.
func EditFile(root, path, oldStr, newStr string) error {
	if oldStr == "" {
		return fmt.Errorf("old_str must not be empty")
	}
	full, err := Resolve(root, path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	content := string(b)
	switch n := strings.Count(content, oldStr); n {
	case 1:
	case 0:
		return fmt.Errorf("old_str not found in %s", path)
	default:
		return fmt.Errorf("old_str occurs %d times in %s; add surrounding context to make it unique", n, path)
	}
	fi, err := os.Stat(full)
	if err != nil {
		return err
	}
	return os.WriteFile(full, []byte(strings.Replace(content, oldStr, newStr, 1)), fi.Mode().Perm())
}
