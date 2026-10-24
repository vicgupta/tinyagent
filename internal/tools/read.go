package tools

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ReadFile returns a file's contents. A positive offset (1-based start line)
// or limit (max lines) returns just that range, ending in a "[lines A-B of N]"
// note when the range isn't the whole file.
func ReadFile(root, path string, offset, limit int) (string, error) {
	if offset < 0 {
		return "", errors.New("offset must be a line number >= 1")
	}
	if limit < 0 {
		return "", errors.New("limit must be >= 0")
	}
	full, err := Resolve(root, path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	s := string(b)
	if offset <= 1 && limit == 0 {
		return s, nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if offset == 0 {
		offset = 1
	}
	if offset > len(lines) {
		if offset == 1 {
			return "", nil // empty file
		}
		return "", fmt.Errorf("offset %d is past the end of the file (%d lines)", offset, len(lines))
	}
	end := len(lines)
	if limit > 0 && offset-1+limit < end {
		end = offset - 1 + limit
	}
	out := strings.Join(lines[offset-1:end], "")
	if offset == 1 && end == len(lines) {
		return out, nil
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + fmt.Sprintf("[lines %d-%d of %d]", offset, end, len(lines)), nil
}
