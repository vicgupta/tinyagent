package tools

import (
	"os"
	"path/filepath"
)

func WriteFile(root, path, content string) error {
	return writeFile(root, path, content, nil)
}

// writeFile is WriteFile that refuses paths fa denies for writing.
func writeFile(root, path, content string, fa *FileAccess) error {
	full, err := Resolve(root, path)
	if err != nil {
		return err
	}
	if err := fa.CheckWrite(full, path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}
