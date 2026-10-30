package memory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// frontMatter is the YAML header of one markdown entry file. The body
// after the closing --- is the entry text.
type frontMatter struct {
	ID               string   `yaml:"id"`
	Status           string   `yaml:"status"`
	CreatedAt        string   `yaml:"created_at"`
	UpdatedAt        string   `yaml:"updated_at"`
	SourceSession    string   `yaml:"source_session,omitempty"`
	Model            string   `yaml:"model,omitempty"`
	Version          string   `yaml:"version,omitempty"`
	UntrustedContext bool     `yaml:"untrusted_context"`
	Tags             []string `yaml:"tags,flow"`
	Replaces         string   `yaml:"replaces,omitempty"`
}

const dirLockName = ".lock"

// MarshalMarkdown renders e as one markdown file: YAML front matter, then
// the text as the body.
func MarshalMarkdown(e Entry) ([]byte, error) {
	fm := frontMatter{
		ID: e.ID, Status: e.Status,
		CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: e.UpdatedAt.UTC().Format(time.RFC3339Nano),
		SourceSession: e.Source.RunID, Model: e.Source.Model, Version: e.Source.Version,
		UntrustedContext: e.Source.UntrustedContext, Tags: e.Tags, Replaces: e.Replaces,
	}
	if fm.Tags == nil {
		fm.Tags = []string{}
	}
	head, err := yaml.Marshal(fm)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(e.Text))
	b.WriteString("\n")
	return b.Bytes(), nil
}

// UnmarshalMarkdown parses one markdown entry file. It accepts CRLF line
// endings, a UTF-8 byte-order mark, blank lines around the body and
// unknown front-matter keys (ignored).
func UnmarshalMarkdown(data []byte) (Entry, error) {
	s := strings.TrimPrefix(string(data), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return Entry{}, errors.New("missing YAML front matter (the file must start with ---)")
	}
	rest := s[4:]
	var head, body string
	if strings.HasPrefix(rest, "---\n") || rest == "---" {
		head, body = "", strings.TrimPrefix(rest, "---")
	} else {
		i := strings.Index(rest, "\n---\n")
		if i < 0 {
			if strings.HasSuffix(rest, "\n---") {
				i = len(rest) - 4
			} else {
				return Entry{}, errors.New("front matter is not closed with ---")
			}
		}
		head, body = rest[:i], rest[i+4:]
	}
	var fm frontMatter
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return Entry{}, fmt.Errorf("front matter: %v", err)
	}
	e := Entry{ID: fm.ID, Status: fm.Status, Text: strings.Join(strings.Fields(body), " "), Replaces: fm.Replaces,
		Source: Source{RunID: fm.SourceSession, Model: fm.Model, Version: fm.Version, UntrustedContext: fm.UntrustedContext},
		Tags:   NormalizeTags(fm.Tags)}
	var err error
	if e.CreatedAt, err = time.Parse(time.RFC3339Nano, fm.CreatedAt); err != nil {
		return Entry{}, fmt.Errorf("created_at %q is not an RFC 3339 time", fm.CreatedAt)
	}
	e.UpdatedAt = e.CreatedAt
	if fm.UpdatedAt != "" {
		if e.UpdatedAt, err = time.Parse(time.RFC3339Nano, fm.UpdatedAt); err != nil {
			return Entry{}, fmt.Errorf("updated_at %q is not an RFC 3339 time", fm.UpdatedAt)
		}
	}
	if e.Text == "" {
		return Entry{}, errors.New("entry text is empty")
	}
	return e, validEntry(e)
}

// loadDir reads every <id>.md file in dir. Other files, dotfiles and
// subfolders are ignored. A file that can't be parsed, or whose id doesn't
// match its name, makes the whole store corrupt.
func loadDir(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".md" {
			continue
		}
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		e, err := UnmarshalMarkdown(data)
		if err != nil {
			return nil, &CorruptError{Path: p, Err: err}
		}
		if e.ID+".md" != name {
			return nil, &CorruptError{Path: p, Err: fmt.Errorf("id %q doesn't match the file name", e.ID)}
		}
		out = append(out, e)
	}
	sortStored(out)
	return out, nil
}

// sortStored orders entries oldest first (by creation, then id), the order
// stores without a sequence keep them in.
func sortStored(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if !es[i].CreatedAt.Equal(es[j].CreatedAt) {
			return es[i].CreatedAt.Before(es[j].CreatedAt)
		}
		return es[i].ID < es[j].ID
	})
}

// updateDir is Update for a markdown folder: changed and new entries are
// written atomically (temp file + rename), removed entries' files deleted.
func updateDir(dir string, fn func([]Entry) ([]Entry, error)) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(dir, dirLockName))
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := loadDir(dir)
	if err != nil {
		return err
	}
	out, err := fn(entries)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, e := range out {
		if err := validEntry(e); err != nil {
			return err
		}
		keep[e.ID] = true
		data, err := MarshalMarkdown(e)
		if err != nil {
			return err
		}
		p := filepath.Join(dir, e.ID+".md")
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err := writeFileAtomic(p, data); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if !keep[e.ID] {
			if err := os.Remove(filepath.Join(dir, e.ID+".md")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
