package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// chatHistoryPath is where chat input is recorded, relative to --cwd.
const chatHistoryPath = "history/chat_history"

// chatHistoryMax caps the recorded entries; older ones are dropped.
const chatHistoryMax = 1000

// chatHistory is the input history the line editor recalls with Up/Down.
// With a path it is also kept in that file (directory 0700, file 0600),
// one strconv.Quote'd entry per line so multi-line messages stay one
// entry. Entries starting with a space are never recorded.
type chatHistory struct {
	path    string
	entries []string
}

// loadChatHistory reads path ("" keeps the history in memory only). A
// missing file is an empty history.
func loadChatHistory(path string) (*chatHistory, error) {
	h := &chatHistory{path: path}
	if path == "" {
		return h, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if e, err := strconv.Unquote(sc.Text()); err == nil {
			h.entries = append(h.entries, e)
		}
	}
	h.trim()
	return h, sc.Err()
}

func (h *chatHistory) trim() {
	if n := len(h.entries) - chatHistoryMax; n > 0 {
		h.entries = append([]string(nil), h.entries[n:]...)
	}
}

// add records line unless it is empty, starts with a space or repeats the
// previous entry, and rewrites the file.
func (h *chatHistory) add(line string) error {
	if h == nil || strings.TrimSpace(line) == "" || strings.HasPrefix(line, " ") {
		return nil
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == line {
		return nil
	}
	h.entries = append(h.entries, line)
	h.trim()
	return h.save()
}

// save rewrites the file through a temporary file in the same directory.
func (h *chatHistory) save() error {
	if h.path == "" {
		return nil
	}
	dir := filepath.Dir(h.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".chat_history-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	for _, e := range h.entries {
		w.WriteString(strconv.Quote(e) + "\n")
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), h.path)
}
