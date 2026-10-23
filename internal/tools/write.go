package tools

import (
	"os"
	"path/filepath"
)

func WriteFile(root, path, content string) error {
	full, err := Resolve(root, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}
