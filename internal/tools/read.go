package tools

import "os"

func ReadFile(root, path string) (string, error) {
	full, err := Resolve(root, path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
