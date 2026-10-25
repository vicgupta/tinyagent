package trajectory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// maxCheckpointBytes bounds parsing and memory use. Larger trajectories remain
// audit logs but cannot be resumed by this format.
const maxCheckpointBytes = 64 << 20 // 64 MiB

// ContinuationNotice is part of the versioned checkpoint contract. It is a
// user message at each recorded resume boundary, never a new system prompt.
const ContinuationNotice = "The previous invocation stopped before this task finished. " +
	"The conversation above is saved history up to the last checkpoint. " +
	"The workspace may have changed since that save, and commands may have produced " +
	"side effects that are not reflected in the transcript. Inspect and revalidate " +
	"the current workspace before continuing. Live processes and shell state are not " +
	"restored; historical process IDs cannot be used to reattach to old processes."

// Load reads a trusted local checkpoint without modifying it. Parse diagnostics
// intentionally omit decoder errors, which can contain transcript values.
func Load(path string) (*Trajectory, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("checkpoint must be a regular file")
	}
	if fi.Size() > maxCheckpointBytes {
		return nil, errors.New("checkpoint exceeds the 64 MiB resume size limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint: %w", err)
	}
	defer f.Close()
	fi, err = f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("checkpoint must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCheckpointBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint: %w", err)
	}
	if len(data) > maxCheckpointBytes {
		return nil, errors.New("checkpoint exceeds the 64 MiB resume size limit")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("checkpoint is empty")
	}
	var t *Trajectory
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil || t == nil {
		return nil, errors.New("invalid checkpoint JSON or unsupported checkpoint fields")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid checkpoint JSON: trailing data")
	}
	if t.SchemaVersion != SchemaVersion {
		return nil, t.Validate()
	}
	// A false loading mode must be explicit, rather than guessed from a
	// missing field. All new files write this field even when it is false.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if _, ok := fields["skip_agents_md"]; !ok || bytes.Equal(fields["skip_agents_md"], []byte("null")) {
		return nil, errors.New("checkpoint is missing AGENTS.md loading metadata; start a fresh task")
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

func unfinished(status string) bool {
	switch status {
	case "", "StepLimitExceeded", "CostLimitExceeded", "TimeLimitExceeded", "Interrupted", "Error":
		return true
	}
	return false
}

// Validate checks a complete model-query boundary, matching IDs within each
// assistant turn. It never repairs or dispatches historical calls.
func (t *Trajectory) Validate() error {
	if t.OutputSchemaDigest != "" || t.OutputSchema != nil {
		return errors.New("runs started with --output-schema cannot be resumed; start a fresh task")
	}
	if t.MCPEnabled || len(t.ExternalTools) > 0 {
		return errors.New("checkpoint used MCP tools and cannot be resumed; start a fresh task")
	}
	if t.SchemaVersion == 0 {
		return errors.New("checkpoint has no resume metadata (it predates --resume); start a fresh task")
	}
	if t.SchemaVersion != SchemaVersion {
		return fmt.Errorf("checkpoint schema version %d is unsupported (want %d)", t.SchemaVersion, SchemaVersion)
	}
	if !filepath.IsAbs(t.Root) || filepath.Clean(t.Root) != t.Root || t.SystemPrompt == "" || len(t.Tools) == 0 || t.ToolSignature == "" {
		return errors.New("checkpoint is missing required root, instruction, or tool compatibility metadata")
	}
	if t.Task == "" || t.Model == "" {
		return errors.New("checkpoint is missing its task or model")
	}
	if t.ExitStatus == "Submitted" || t.ExitStatus == "Completed" {
		return errors.New("checkpoint already finished; start a fresh task")
	}
	if !unfinished(t.ExitStatus) {
		return errors.New("checkpoint has an unsupported exit status")
	}
	if t.Steps < 0 || math.IsNaN(t.Cost) || math.IsInf(t.Cost, 0) || t.Cost < 0 || t.InputTokens < 0 || t.OutputTokens < 0 || t.CacheReadTokens < 0 || t.CacheWriteTokens < 0 || t.CacheReadTokens > t.InputTokens || t.CacheWriteTokens > t.InputTokens-t.CacheReadTokens {
		return errors.New("checkpoint has invalid accounting counters")
	}
	if len(t.Compactions) > 0 {
		return errors.New("checkpoint was compacted; resuming compacted model views is not yet supported; start a fresh task")
	}
	if len(t.Messages) < 2 || t.Messages[0].Role != "system" || t.Messages[0].Content != t.SystemPrompt || len(t.Messages[0].ToolCalls) != 0 || t.Messages[0].ToolCallID != "" {
		return errors.New("checkpoint has an invalid system prompt")
	}
	if t.Messages[1].Role != "user" || t.Messages[1].Content != t.Task || len(t.Messages[1].ToolCalls) != 0 || t.Messages[1].ToolCallID != "" {
		return errors.New("checkpoint has an invalid task message")
	}
	boundaries := map[int]*ResumeInfo{}
	end, steps := len(t.Messages), t.Steps
	for b := t.Resume; b != nil; b = b.Previous {
		if b.Message < 2 || b.Message >= end || b.Steps < 0 || b.Steps > steps || !unfinished(b.ExitStatus) || b.Source == "" {
			return errors.New("checkpoint has invalid resume provenance")
		}
		boundaries[b.Message] = b
		end, steps = b.Message, b.Steps
	}
	known := map[string]bool{}
	for _, name := range t.Tools {
		if name == "" || known[name] {
			return errors.New("checkpoint has invalid tool metadata")
		}
		known[name] = true
	}
	steps = 0
	for i := 2; i < len(t.Messages); {
		m := t.Messages[i]
		if b, ok := boundaries[i]; ok {
			if b.Steps != steps || m.Role != "user" || m.Content != ContinuationNotice || len(m.ToolCalls) != 0 || m.ToolCallID != "" {
				return errors.New("checkpoint has an invalid continuation notice")
			}
			delete(boundaries, i)
			i++
			continue
		}
		if m.Role != "assistant" || m.ToolCallID != "" || len(m.ToolCalls) == 0 || strings.Contains(m.Content, "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT") {
			return errors.New("checkpoint history is not an unfinished assistant/tool boundary")
		}
		ids := map[string]bool{}
		for _, c := range m.ToolCalls {
			var args map[string]json.RawMessage
			if c.ID == "" || ids[c.ID] || c.Type != "function" || !known[c.Function.Name] || json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil {
				return errors.New("checkpoint has an invalid or duplicate tool call")
			}
			ids[c.ID] = true
		}
		i++
		for _, c := range m.ToolCalls {
			if i >= len(t.Messages) {
				return errors.New("checkpoint ends mid-turn: missing tool result")
			}
			r := t.Messages[i]
			if r.Role != "tool" || r.ToolCallID != c.ID || len(r.ToolCalls) != 0 {
				return errors.New("checkpoint has a missing, duplicate, orphaned, or out-of-order tool result")
			}
			i++
		}
		steps++
	}
	if len(boundaries) != 0 {
		return errors.New("checkpoint resume boundary is inside a tool turn")
	}
	if steps != t.Steps {
		return errors.New("checkpoint steps do not match the recorded history")
	}
	return nil
}
