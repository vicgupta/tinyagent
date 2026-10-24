// Package tools implements the agent's file and shell tools, sandboxed to a root directory.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
)

// Executor runs tool calls against files under Root.
type Executor struct {
	Root           string // absolute working directory
	BashTimeout    time.Duration
	MaxOutputChars int // 0 = unlimited
}

type args struct {
	Path    string      `json:"path"`
	Content string      `json:"content"`
	OldStr  string      `json:"old_str"`
	NewStr  string      `json:"new_str"`
	Pattern string      `json:"pattern"`
	Command string      `json:"command"`
	Offset  json.Number `json:"offset"`
	Limit   json.Number `json:"limit"`
}

// Specs returns the OpenAI tool definitions for all tools.
func Specs() []model.ToolSpec {
	return []model.ToolSpec{
		spec("read_file", "Read a file's contents. For a large file, pass offset and limit to read just a range of lines.",
			props{"path": "File path relative to the working directory", "offset": "Optional 1-based line to start at", "limit": "Optional maximum number of lines to return"}, "path"),
		spec("write_file", "Write content to a file, creating parent directories. Overwrites existing files.",
			props{"path": "File path", "content": "Full file content"}, "path", "content"),
		spec("edit_file", "Replace exactly one occurrence of old_str with new_str in a file. Fails if old_str occurs zero or multiple times; include more context to make it unique.",
			props{"path": "File path", "old_str": "Exact text to replace", "new_str": "Replacement text"}, "path", "old_str", "new_str"),
		spec("list_files", "List directory entries. With a glob pattern (e.g. *.go, or a pattern containing /), search recursively and return matching file paths.",
			props{"path": "Directory (default .)", "pattern": "Optional glob pattern"}),
		spec("grep", "Search files recursively for a regular expression. Returns file:line: content.",
			props{"pattern": "Go regular expression", "path": "File or directory to search (default .)"}, "pattern"),
		spec("bash", "Run a command with bash -c in the working directory. Each call is a fresh process. Returns the exit code and combined stdout/stderr.",
			props{"command": "Shell command"}, "command"),
	}
}

type props map[string]string

// intProps are the parameters that take integers; the rest take strings.
var intProps = map[string]bool{"offset": true, "limit": true}

func spec(name, desc string, p props, required ...string) model.ToolSpec {
	properties := map[string]any{}
	for k, d := range p {
		typ := "string"
		if intProps[k] {
			typ = "integer"
		}
		properties[k] = map[string]any{"type": typ, "description": d}
	}
	if required == nil {
		required = []string{}
	}
	return model.ToolSpec{
		Type: "function",
		Function: model.FunctionSpec{
			Name:        name,
			Description: desc,
			Parameters:  map[string]any{"type": "object", "properties": properties, "required": required},
		},
	}
}

// Run executes one tool call and returns text for the model. Tool failures are
// reported as text so the model can recover; Run never returns an error.
func (e *Executor) Run(ctx context.Context, call model.ToolCall) string {
	out, err := e.run(ctx, call.Function.Name, call.Function.Arguments)
	if err != nil {
		out = "error: " + err.Error()
	}
	return truncate(out, e.MaxOutputChars)
}

func (e *Executor) run(ctx context.Context, name, raw string) (string, error) {
	var a args
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return "", fmt.Errorf("invalid arguments JSON: %w", err)
		}
	}
	switch name {
	case "read_file":
		offset, err := intArg("offset", a.Offset)
		if err != nil {
			return "", err
		}
		limit, err := intArg("limit", a.Limit)
		if err != nil {
			return "", err
		}
		return ReadFile(e.Root, a.Path, offset, limit)
	case "write_file":
		return "ok", WriteFile(e.Root, a.Path, a.Content)
	case "edit_file":
		return "ok", EditFile(e.Root, a.Path, a.OldStr, a.NewStr)
	case "list_files":
		return ListFiles(e.Root, a.Path, a.Pattern)
	case "grep":
		return Grep(e.Root, a.Pattern, a.Path)
	case "bash":
		return Bash(ctx, e.Root, a.Command, e.BashTimeout)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// intArg converts an optional integer argument, which models send either as a
// JSON number or as a string, to an int; omitted means 0.
func intArg(name string, n json.Number) (int, error) {
	if n == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(string(n))
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %s", name, n)
	}
	return v, nil
}

// Resolve maps a path relative to root (or absolute inside root) to an
// absolute path, rejecting anything that escapes root. The check is lexical;
// symlinks inside root that point outside it are followed.
func Resolve(root, path string) (string, error) {
	if path == "" {
		path = "."
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, path)
	}
	full = filepath.Clean(full)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", path)
	}
	return full, nil
}

// rel returns path relative to root, with forward slashes, for display.
func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

// skipDir reports whether a directory should be skipped during recursive walks.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".venv", "__pycache__":
		return true
	}
	return false
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := max / 2
	return fmt.Sprintf("%s\n\n... [%d characters elided] ...\n\n%s", s[:half], len(s)-2*half, s[len(s)-half:])
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
