// Package tools implements the agent's file and shell tools, sandboxed to a root directory.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
)

func isBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// Executor runs tool calls against files under Root.
type Executor struct {
	Root           string // absolute working directory
	ReadOnly       bool   // permits only read_file, list_files, and grep
	BashTimeout    time.Duration
	MaxOutputChars int // 0 = unlimited
	// MaxProcesses bounds concurrently running background processes
	// (0 = DefaultMaxProcesses).
	MaxProcesses int
	// ProcessBufferBytes caps the output kept per background process
	// (0 = DefaultProcessBufferBytes); older output is dropped.
	ProcessBufferBytes int
	// Env decides where bash and start_process commands run. nil means
	// Local{}: on the host, unsandboxed.
	Env Environment
	// Stream receives bash output as it is produced when a call sets
	// stream: true. nil discards it (the result is unchanged either way).
	Stream        io.Writer
	ExternalSpecs []model.ToolSpec
	// ExternalCall runs a tool from ExternalSpecs. ok is false when name is
	// not an external tool; isErr marks a failed call.
	ExternalCall func(ctx context.Context, name, args string) (out string, isErr, ok bool)
	// Plan offers the opt-in update_plan tool (also in read-only mode). The
	// agent handles its calls; the executor only advertises it.
	Plan bool
	// Images offers the opt-in view_image tool (also in read-only mode) and
	// lets ExternalCallImages pass MCP images through. MaxImageBytes caps
	// one image (0 = DefaultMaxImageBytes).
	Images        bool
	MaxImageBytes int
	// ExternalCallImages, when set and Images is on, replaces ExternalCall
	// and also returns the result's images.
	ExternalCallImages func(ctx context.Context, name, args string) (out string, images []model.Image, isErr, ok bool)
	// Skills, when it holds at least one skill, adds the read-only skill
	// tool (also in read-only mode). nil or empty offers nothing.
	Skills *skills.Set
	// ApplyPatch offers the opt-in apply_patch tool (never in read-only
	// mode).
	ApplyPatch bool
	// Delegate offers the opt-in delegate tool (also in read-only mode).
	// The agent loop runs its calls; the executor never does.
	Delegate bool
	// OutputFiles, when set, saves the full text of over-limit results to a
	// per-run file and keeps that directory out of grep and list_files
	// walks. nil keeps plain truncation.
	OutputFiles *OutputFiles
	// FileAccess denies the built-in file tools access to configured paths
	// (file_access config). nil denies nothing. It does not apply to bash,
	// start_process, hooks or MCP servers.
	FileAccess *FileAccess

	procs registry
}

// outputSkip is the directory recursive walks skip: the saved-output
// directory when OutputFiles is on.
func (e *Executor) outputSkip() string {
	if e.OutputFiles == nil {
		return ""
	}
	return e.OutputFiles.Dir
}

// maxBashTimeoutSeconds caps the per-call bash timeout a model may request.
const maxBashTimeoutSeconds = 600

type args struct {
	Path         string          `json:"path"`
	Content      string          `json:"content"`
	OldStr       string          `json:"old_str"`
	NewStr       string          `json:"new_str"`
	Pattern      string          `json:"pattern"`
	Include      string          `json:"include"`
	Command      string          `json:"command"`
	Offset       json.Number     `json:"offset"`
	Limit        json.Number     `json:"limit"`
	Timeout      json.Number     `json:"timeout"`
	ReplaceAll   json.RawMessage `json:"replace_all"`
	IgnoreCase   json.RawMessage `json:"ignore_case"`
	FixedStrings json.RawMessage `json:"fixed_strings"`
	Stream       json.RawMessage `json:"stream"`
	ID           string          `json:"id"`
	Tail         json.Number     `json:"tail"`
	ContextLines json.RawMessage `json:"context_lines"`
	Name         string          `json:"name"`
	File         string          `json:"file"`
	Input        string          `json:"input"`
}

// Specs returns the OpenAI tool definitions for all tools.
func Specs(readOnly ...bool) []model.ToolSpec {
	specs := []model.ToolSpec{
		spec("read_file", "Read a file's contents. For a large file, pass offset and limit to read just a range of lines.",
			props{"path": "File path relative to the working directory", "offset": "Optional 1-based line to start at", "limit": "Optional maximum number of lines to return"}, "path"),
		spec("write_file", "Write content to a file, creating parent directories. Overwrites existing files.",
			props{"path": "File path", "content": "Full file content"}, "path", "content"),
		spec("edit_file", "Replace exactly one occurrence of old_str with new_str in a file. Fails if old_str occurs zero or multiple times; include more context to make it unique, or set replace_all to replace every occurrence.",
			props{"path": "File path", "old_str": "Exact text to replace", "new_str": "Replacement text", "replace_all": "Optional: replace every occurrence instead of exactly one"}, "path", "old_str", "new_str"),
		spec("list_files", "List directory entries. With a glob pattern (e.g. *.go, or a pattern containing /), search recursively and return matching file paths.",
			props{"path": "Directory (default .)", "pattern": "Optional glob pattern"}),
		spec("grep", "Search files recursively for a Go regular expression (default), or literal text with fixed_strings: true. Returns file:line: content; context_lines optionally adds surrounding lines.",
			props{"pattern": "Go regular expression by default; literal text when fixed_strings is true", "path": "File or directory to search (default .)", "include": "Optional glob to filter files, e.g. *.go (a glob with / matches paths relative to path)", "ignore_case": "Optional: match file contents case-insensitively (default false)", "fixed_strings": "Optional: treat the entire pattern as literal text instead of a regular expression (default false)", "context_lines": "Optional surrounding lines before and after each match (default 0; integer from 0 to 20)"}, "pattern"),
		spec("bash", "Run a command with bash -c in the working directory and wait for it to finish. Each call is a fresh process. Returns the exit code and combined stdout/stderr. For servers, watchers, or anything long-running, use start_process instead.",
			props{"command": "Shell command", "timeout": "Optional timeout in seconds for this command (default: the configured bash timeout, max 600)", "stream": "Optional: also show output to the user live as it is produced (default false); the returned result is the same"}, "command"),
		spec("start_process", "Start a command with bash -c in the background, in the working directory, and return its process id plus its first output. It keeps running across steps until stop_process or the end of the run.",
			props{"command": "Shell command, e.g. a dev server or watch mode"}, "command"),
		spec("read_process", "Return a background process's output produced since the last read (never repeated), and whether it is still running.",
			props{"id": "Process id from start_process", "timeout": "Optional milliseconds to wait for new output if there is none yet (default 0, max 60000)", "tail": "Optional: return only the last N lines of the new output"}, "id"),
		spec("stop_process", "Kill a background process and all its children, and return its exit status and unread output.",
			props{"id": "Process id from start_process", "tail": "Optional: return only the last N lines of the unread output"}, "id"),
	}
	if len(readOnly) > 0 && readOnly[0] {
		allowed := map[string]bool{"read_file": true, "list_files": true, "grep": true}
		filtered := make([]model.ToolSpec, 0, len(allowed))
		for _, s := range specs {
			if allowed[s.Function.Name] {
				filtered = append(filtered, s)
			}
		}
		return filtered
	}
	return specs
}

type props map[string]string

// intProps and boolProps are the parameters that take integers and booleans;
// the rest take strings.
var (
	intProps  = map[string]bool{"offset": true, "limit": true, "timeout": true, "tail": true, "context_lines": true}
	boolProps = map[string]bool{"replace_all": true, "ignore_case": true, "fixed_strings": true, "stream": true}
)

func spec(name, desc string, p props, required ...string) model.ToolSpec {
	properties := map[string]any{}
	for k, d := range p {
		typ := "string"
		if intProps[k] {
			typ = "integer"
		} else if boolProps[k] {
			typ = "boolean"
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
	out, _ := e.RunWithError(ctx, call)
	return out
}

// RunWithError executes a tool call and reports whether execution failed.
// The returned text is still suitable for passing back to the model.
func (e *Executor) RunWithError(ctx context.Context, call model.ToolCall) (string, bool) {
	out, _, isErr := e.RunWithImages(ctx, call)
	return out, isErr
}

// RunWithImages is RunWithError plus any images the result carries (only
// with Images on). The text already holds a placeholder for each image.
func (e *Executor) RunWithImages(ctx context.Context, call model.ToolCall) (string, []model.Image, bool) {
	r := e.RunResult(ctx, call)
	return r.Text, r.Images, r.IsError
}

// Result is one tool call's outcome for the model.
type Result struct {
	Text    string
	Images  []model.Image
	IsError bool
	// OutputFile is the saved full output (relative to Root, slash
	// separated) when OutputFiles is on and the text was over the limit.
	OutputFile  string
	OutputBytes int
	// Notice is the line ending Text that points at the saved file (or, for
	// read_file, at the rest of the file); empty when none was added.
	Notice string
}

// RunResult is RunWithImages plus where an over-limit result was saved.
func (e *Executor) RunResult(ctx context.Context, call model.ToolCall) Result {
	if e.Images && !e.ReadOnly && e.ExternalCallImages != nil {
		if out, images, isErr, ok := e.ExternalCallImages(ctx, call.Function.Name, call.Function.Arguments); ok {
			r := e.limit(call, out, isErr)
			r.Images = images
			return r
		}
	}
	if e.Images && call.Function.Name == ViewImageToolName {
		var a args
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &a); err != nil {
				return Result{Text: "error: invalid arguments JSON: " + err.Error(), IsError: true}
			}
		}
		out, im, err := viewImage(e.Root, a.Path, e.MaxImageBytes, e.FileAccess)
		if err != nil {
			return Result{Text: truncate("error: "+err.Error(), e.MaxOutputChars), IsError: true}
		}
		return Result{Text: out, Images: []model.Image{*im}}
	}
	// External tools are never dispatched in read-only mode: the check in
	// run() below then rejects them with the read-only error, matching
	// Catalog(), which also excludes them there.
	if !e.ReadOnly && e.ExternalCall != nil {
		if out, isErr, ok := e.ExternalCall(ctx, call.Function.Name, call.Function.Arguments); ok {
			return e.limit(call, out, isErr)
		}
	}
	out, err := e.run(ctx, call.Function.Name, call.Function.Arguments)
	if err != nil {
		out = "error: " + err.Error()
	}
	return e.limit(call, out, err != nil)
}

// Catalog returns the fixed built-in and explicitly configured external tools.
// In read-only mode it returns only the read-only built-ins: external MCP
// tools must never be offered (or dispatched, see Run) when only
// read_file, list_files, and grep are available.
func (e *Executor) Catalog() []model.ToolSpec {
	out := Specs(e.ReadOnly)
	if e.Plan {
		out = append(out, PlanSpec())
	}
	if e.Images {
		out = append(out, ViewImageSpec())
	}
	if e.ApplyPatch && !e.ReadOnly {
		out = append(out, ApplyPatchSpec())
	}
	if e.Delegate {
		out = append(out, DelegateSpec())
	}
	if e.Skills.Len() > 0 {
		out = append(out, e.Skills.Spec())
	}
	if e.ReadOnly {
		return out
	}
	return append(out, e.ExternalSpecs...)
}

// PlanToolName is the opt-in plan tool.
const PlanToolName = "update_plan"

// PlanSpec returns the update_plan definition.
func PlanSpec() model.ToolSpec {
	return model.ToolSpec{
		Type: "function",
		Function: model.FunctionSpec{
			Name: PlanToolName,
			Description: "Replace your whole task plan. The plan is saved with the checkpoint and shown to you every step, " +
				"even after context compaction. Keep at most one item in_progress. Returns the rendered plan.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"items": map[string]any{
						"type":        "array",
						"description": "The complete plan, in order (at most 50 items)",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":     map[string]any{"type": "string", "description": "Stable item id"},
								"text":   map[string]any{"type": "string", "description": "What to do (at most 500 characters)"},
								"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done", "blocked"}},
							},
							"required": []string{"id", "text", "status"},
						},
					},
					"note": map[string]any{"type": "string", "description": "Optional short note about the plan"},
				},
				"required": []string{"items"},
			},
		},
	}
}

// Permits reports whether a call to name would be dispatched: name is in the
// catalog, which in read-only mode holds only the read-only built-ins.
func (e *Executor) Permits(name string) bool {
	for _, s := range e.Catalog() {
		if s.Function.Name == name {
			return true
		}
	}
	return false
}

func (e *Executor) run(ctx context.Context, name, raw string) (string, error) {
	if name == PlanToolName && e.Plan {
		return "", errors.New("update_plan is handled by the agent loop")
	}
	if name == DelegateToolName && e.Delegate {
		return "", errors.New("delegate is handled by the agent loop")
	}
	skillTool := name == skills.ToolName && e.Skills.Len() > 0
	if e.ReadOnly && !skillTool && (name != "read_file" && name != "list_files" && name != "grep") {
		return "", fmt.Errorf("tool %q is unavailable in read-only mode; only read_file, list_files, and grep are allowed", name)
	}
	var a args
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return "", fmt.Errorf("invalid arguments JSON: %w", err)
		}
	}
	if skillTool {
		return e.Skills.Load(a.Name, a.File)
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
		return readFile(e.Root, a.Path, offset, limit, e.FileAccess)
	case "write_file":
		return "ok", writeFile(e.Root, a.Path, a.Content, e.FileAccess)
	case "edit_file":
		replaceAll, err := boolArg("replace_all", a.ReplaceAll)
		if err != nil {
			return "", err
		}
		n, err := editFile(e.Root, a.Path, a.OldStr, a.NewStr, replaceAll, e.FileAccess)
		if err != nil || !replaceAll {
			return "ok", err
		}
		return fmt.Sprintf("ok (%d replacements)", n), nil
	case ApplyPatchToolName:
		if !e.ApplyPatch {
			return "", fmt.Errorf("unknown tool %q", name)
		}
		return applyPatch(e.Root, a.Input, e.FileAccess)
	case "list_files":
		return listFiles(e.Root, a.Path, a.Pattern, e.outputSkip(), e.FileAccess)
	case "grep":
		contextLines, err := contextLinesArg(a.ContextLines)
		if err != nil {
			return "", err
		}
		if contextLines < 0 || contextLines > maxGrepContextLines {
			return "", fmt.Errorf("context_lines must be between 0 and %d, got %d", maxGrepContextLines, contextLines)
		}
		ignoreCase, err := boolArg("ignore_case", a.IgnoreCase)
		if err != nil {
			return "", err
		}
		fixedStrings, err := boolArg("fixed_strings", a.FixedStrings)
		if err != nil {
			return "", err
		}
		return grepWithContext(e.Root, a.Pattern, a.Path, a.Include, ignoreCase, fixedStrings, contextLines, e.outputSkip(), e.FileAccess)
	case "bash":
		secs, err := intArg("timeout", a.Timeout)
		if err != nil {
			return "", err
		}
		if secs < 0 || secs > maxBashTimeoutSeconds {
			return "", fmt.Errorf("timeout must be between 1 and %d seconds, got %d", maxBashTimeoutSeconds, secs)
		}
		timeout := e.BashTimeout
		if secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
		stream, err := boolArg("stream", a.Stream)
		if err != nil {
			return "", err
		}
		var live io.Writer
		if stream {
			live = e.Stream
		}
		return bashStream(ctx, e.Env, e.Root, a.Command, timeout, live)
	case "start_process":
		return e.StartProcess(a.Command)
	case "read_process":
		ms, err := intArg("timeout", a.Timeout)
		if err != nil {
			return "", err
		}
		tail, err := intArg("tail", a.Tail)
		if err != nil {
			return "", err
		}
		return e.ReadProcess(ctx, a.ID, ms, tail)
	case "stop_process":
		tail, err := intArg("tail", a.Tail)
		if err != nil {
			return "", err
		}
		if tail < 0 {
			return "", fmt.Errorf("tail must be positive, got %d", tail)
		}
		return e.StopProcess(a.ID, tail)
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

func contextLinesArg(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			n = json.Number(value)
		} else {
			return 0, fmt.Errorf("context_lines must be a whole number from 0 to %d", maxGrepContextLines)
		}
	}
	v, err := intArg("context_lines", n)
	if err != nil {
		return 0, fmt.Errorf("context_lines must be a whole number from 0 to %d", maxGrepContextLines)
	}
	return v, nil
}

// boolArg converts an optional boolean argument to a bool; omitted or null
// means false.
func boolArg(name string, raw json.RawMessage) (bool, error) {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, fmt.Errorf("%s must be true or false, got %s", name, raw)
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

// Truncate caps s at max characters, keeping its head and tail; 0 means
// unlimited. It is the same cap applied to tool output.
func Truncate(s string, max int) string { return truncate(s, max) }

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
