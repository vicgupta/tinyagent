package tools

import (
	"os"
	"path/filepath"
)

func WriteFile(root, path, content string) error {
	return writeFile(root, path, content, nil)
}

// writeFile is WriteFile that refuses paths the scope denies for writing.
func writeFile(root, path, content string, sc *scope) error {
	full, err := sc.resolve(root, path)
	if err != nil {
		return err
	}
	if err := sc.checkWrite(full, path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}
