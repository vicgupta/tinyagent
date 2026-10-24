package main

import (
	"errors"
	"strings"
	"testing"
)

func TestReadTask(t *testing.T) {
	cases := []struct {
		task, stdin, want string
	}{
		{"fix the bug", "ignored", "fix the bug"},
		{"-", "line one\nline two\n", "line one\nline two"},
		{"-", "  padded  \n\n", "padded"},
	}
	for _, c := range cases {
		got, err := readTask(c.task, strings.NewReader(c.stdin))
		if err != nil || got != c.want {
			t.Errorf("readTask(%q, %q) = %q, %v; want %q", c.task, c.stdin, got, err, c.want)
		}
	}
}

func TestReadTaskEmptyStdin(t *testing.T) {
	if _, err := readTask("-", strings.NewReader(" \n")); err == nil {
		t.Error("want an error for an empty task on stdin")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestReadTaskStdinError(t *testing.T) {
	if _, err := readTask("-", errReader{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want the read error, got %v", err)
	}
}
