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

	"github.com/vicgupta/tinyagent/internal/model"
)

// ToolCallRef is a recorded tool call.
type ToolCallRef = model.ToolCall

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

// FollowUpNotice is part of the versioned checkpoint contract. It starts the
// user message at each follow-up boundary, where a finished run is continued
// with a new instruction; the instruction follows it.
const FollowUpNotice = "The previous invocation finished. The conversation above is its saved history. " +
	"The workspace may have changed since it finished. Inspect and revalidate the current " +
	"workspace before relying on earlier observations. Live processes and shell state are not " +
	"restored. The user's follow-up task is below."

// Finished reports whether status is a successful termination that can only
// be continued with a follow-up task.
func Finished(status string) bool {
	return status == "Submitted" || status == "Completed"
}

// finalShape reports whether m is the final assistant message of a run that
// ended with status: no tool calls, with the submit marker for Submitted and
// without it for Completed.
func finalShape(m model.Message, status string) bool {
	if m.Role != "assistant" || m.ToolCallID != "" || len(m.ToolCalls) != 0 {
		return false
	}
	marker := strings.Contains(m.Content, "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT")
	switch status {
	case "Submitted":
		return marker
	case "Completed":
		return !marker
	}
	return false
}

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
	if t.SchemaVersion != 1 && t.SchemaVersion != SchemaVersion {
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
	case "", "StepLimitExceeded", "CostLimitExceeded", "TimeLimitExceeded", "Interrupted", "Error", "RepeatedFormatError":
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
	if t.SchemaVersion != 1 && t.SchemaVersion != SchemaVersion {
		return fmt.Errorf("checkpoint schema version %d is unsupported (want 1 or %d)", t.SchemaVersion, SchemaVersion)
	}
	if t.SchemaVersion == 1 && (len(t.PendingCalls) > 0 || len(t.Processes) > 0 || t.PlanEnabled || t.Plan != nil || len(t.PlanHistory) > 0 || t.ImagesEnabled || t.ApplyPatchEnabled || len(t.ToolOutputFiles) > 0 || t.SubagentsEnabled || len(t.Subagents) > 0 || t.SubagentSteps > 0 || t.WebFetchEnabled || t.LSPEnabled || t.ScopedAgentsMDEnabled || len(t.ScopedAgentsMDLoads) > 0) {
		return errors.New("schema version 1 checkpoint has version 2 fields")
	}
	// Tool-result images need the images setting; user messages may carry
	// --image task attachments, each with its label line.
	for _, m := range t.Messages {
		switch {
		case len(m.Images) == 0:
		case m.Role == "tool" && !t.ImagesEnabled:
			return errors.New("checkpoint has images but the images setting was not enabled")
		case m.Role == "user" && !strings.HasSuffix(m.Content, model.AttachmentLabels(m.Images)):
			return errors.New("checkpoint has image attachments without their labels")
		case m.Role != "tool" && m.Role != "user":
			return errors.New("checkpoint has images outside tool results and user messages")
		}
	}
	if err := t.validatePlan(); err != nil {
		return err
	}
	if !filepath.IsAbs(t.Root) || filepath.Clean(t.Root) != t.Root || t.SystemPrompt == "" || len(t.Tools) == 0 || t.ToolSignature == "" {
		return errors.New("checkpoint is missing required root, instruction, or tool compatibility metadata")
	}
	if t.Task == "" || t.Model == "" {
		return errors.New("checkpoint is missing its task or model")
	}
	finished := Finished(t.ExitStatus)
	if !finished && !unfinished(t.ExitStatus) {
		return errors.New("checkpoint has an unsupported exit status")
	}
	if t.StopContinuations != 0 || t.StopReason != "" {
		return errors.New("checkpoint was sent back to work by a stop hook; runs continued by stop hooks cannot be resumed yet; start a fresh task")
	}
	if t.Steps < 0 || math.IsNaN(t.Cost) || math.IsInf(t.Cost, 0) || t.Cost < 0 || t.InputTokens < 0 || t.OutputTokens < 0 || t.CacheReadTokens < 0 || t.CacheWriteTokens < 0 || t.CacheReadTokens > t.InputTokens || t.CacheWriteTokens > t.InputTokens-t.CacheReadTokens {
		return errors.New("checkpoint has invalid accounting counters")
	}
	if len(t.Messages) < 2 || t.Messages[0].Role != "system" || t.Messages[0].Content != t.SystemPrompt || len(t.Messages[0].ToolCalls) != 0 || t.Messages[0].ToolCallID != "" {
		return errors.New("checkpoint has an invalid system prompt")
	}
	if t.Messages[1].Role != "user" || t.Messages[1].Content != t.Task+model.AttachmentLabels(t.Messages[1].Images) || len(t.Messages[1].ToolCalls) != 0 || t.Messages[1].ToolCallID != "" {
		return errors.New("checkpoint has an invalid task message")
	}
	boundaries := map[int]*ResumeInfo{}
	end, steps := len(t.Messages), t.Steps
	for b := t.Resume; b != nil; b = b.Previous {
		okStatus := unfinished(b.ExitStatus)
		if b.FollowUp {
			okStatus = Finished(b.ExitStatus)
		}
		if (b.FollowUp && b.Message < 3) || b.Message < 2 || b.Message >= end || b.Steps < 0 || b.Steps > steps || !okStatus || b.Source == "" {
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
	if t.FormatErrors < 0 || t.ConsecutiveFormatErrors < 0 || t.ConsecutiveFormatErrors > t.Steps || len(t.FormatErrorMessages) != t.FormatErrors {
		return errors.New("checkpoint has invalid format-error counters")
	}
	if t.Truncations < 0 || t.ConsecutiveTruncations < 0 || t.ConsecutiveTruncations > t.Truncations || t.Truncations > t.Steps || t.MaxOutputTokens < 0 {
		return errors.New("checkpoint has invalid truncation counters")
	}
	markers := map[int]bool{}
	for _, idx := range t.FormatErrorMessages {
		if idx < 2 || idx >= len(t.Messages) || markers[idx] || t.Messages[idx].Role != "tool" {
			return errors.New("checkpoint has invalid format-error markers")
		}
		markers[idx] = true
	}
	steps = 0
	pendingOK := false
	finalAt := -1 // index of the last final assistant message before a follow-up
	for i := 2; i < len(t.Messages); {
		m := t.Messages[i]
		if b, ok := boundaries[i]; ok {
			notice := ContinuationNotice
			if b.FollowUp {
				notice = FollowUpNotice
			}
			if b.Steps != steps || m.Role != "user" || !strings.HasPrefix(m.Content, notice) || len(m.ToolCalls) != 0 || m.ToolCallID != "" || (!b.FollowUp && len(m.Images) > 0) {
				return errors.New("checkpoint has an invalid continuation notice")
			}
			if b.FollowUp && finalAt != i-1 {
				return errors.New("checkpoint history before a follow-up is not a finished assistant turn")
			}
			delete(boundaries, i)
			i++
			continue
		}
		// A final assistant message is valid only as the last message of a
		// finished run, or right before the follow-up boundary that
		// continued it.
		if next, ok := boundaries[i+1]; ok && next.FollowUp {
			if !finalShape(m, next.ExitStatus) {
				return errors.New("checkpoint history before a follow-up is not a finished assistant turn")
			}
			finalAt = i
			steps++
			i++
			continue
		}
		if finished && i == len(t.Messages)-1 {
			if !finalShape(m, t.ExitStatus) {
				return errors.New("finished checkpoint does not end with a final assistant message")
			}
			steps++
			i++
			continue
		}
		if m.Role != "assistant" || m.ToolCallID != "" || len(m.ToolCalls) == 0 || strings.Contains(m.Content, "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT") {
			return errors.New("checkpoint history is not an unfinished assistant/tool boundary")
		}
		ids := map[string]bool{}
		for k, c := range m.ToolCalls {
			var args map[string]json.RawMessage
			// A malformed call is valid history when its result was a
			// format-error correction (the tool never ran).
			malformed := !known[c.Function.Name] || json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil
			if malformed {
				r := i + 1 + k
				malformed = !markers[r] || r >= len(t.Messages) || !strings.HasPrefix(t.Messages[r].Content, "format error:")
			}
			if c.ID == "" || ids[c.ID] || c.Type != "function" || malformed {
				return errors.New("checkpoint has an invalid or duplicate tool call")
			}
			ids[c.ID] = true
		}
		i++
		for k, c := range m.ToolCalls {
			if i >= len(t.Messages) {
				if !pendingMatches(t.PendingCalls, m.ToolCalls[k:]) {
					return errors.New("checkpoint ends mid-turn: missing tool result")
				}
				pendingOK = true
				break
			}
			r := t.Messages[i]
			if r.Role != "tool" || r.ToolCallID != c.ID || len(r.ToolCalls) != 0 {
				return errors.New("checkpoint has a missing, duplicate, orphaned, or out-of-order tool result")
			}
			i++
		}
		steps++
	}
	if len(t.PendingCalls) > 0 && !pendingOK {
		return errors.New("checkpoint has pending tool calls that do not match its last tool turn")
	}
	if err := t.validateCompactions(); err != nil {
		return err
	}
	if len(boundaries) != 0 {
		return errors.New("checkpoint resume boundary is inside a tool turn")
	}
	if steps != t.Steps {
		return errors.New("checkpoint steps do not match the recorded history")
	}
	if finished && (len(t.Messages) < 3 || !finalShape(t.Messages[len(t.Messages)-1], t.ExitStatus)) {
		return errors.New("finished checkpoint does not end with a final assistant message")
	}
	return nil
}

// pendingMatches reports whether pending names exactly the calls without
// results, in order.
func pendingMatches(pending []PendingCall, calls []ToolCallRef) bool {
	if len(pending) == 0 || len(pending) != len(calls) {
		return false
	}
	for i, c := range calls {
		if pending[i].ID != c.ID || pending[i].Name != c.Function.Name {
			return false
		}
	}
	return true
}

// validateCompactions checks that every recorded view can be rebuilt from
// Messages: indexes in range and increasing, the system prompt and task
// first, and a summary placeholder (-1) only right after them, backed by a
// recorded summary.
func (t *Trajectory) validateCompactions() error {
	summary := false
	for _, c := range t.Compactions {
		bad := errors.New("checkpoint has an invalid compaction record")
		if c.Step < 0 || c.Step > t.Steps || len(c.View) < 2 || c.View[0] != 0 || c.View[1] != 1 {
			return bad
		}
		if c.Summary != "" {
			summary = true
		}
		prev := 1
		for k, ix := range c.View[2:] {
			if ix == -1 {
				if k != 0 || !summary {
					return bad
				}
				continue
			}
			if ix <= prev || ix >= len(t.Messages) {
				return bad
			}
			prev = ix
		}
		for _, ix := range append(append([]int(nil), c.Elided...), c.Summarized...) {
			if ix < 2 || ix >= len(t.Messages) {
				return bad
			}
		}
		for _, ix := range c.Elided {
			if t.Messages[ix].Role != "tool" {
				return bad
			}
		}
	}
	return nil
}

// Plan limits enforced by update_plan and checkpoint validation.
const (
	MaxPlanItems    = 50
	MaxPlanItemText = 500
	MaxPlanNote     = 2000
)

// ValidatePlanItems checks one complete plan.
func ValidatePlanItems(items []PlanItem, note string) error {
	if len(items) > MaxPlanItems {
		return fmt.Errorf("plan has %d items; the limit is %d", len(items), MaxPlanItems)
	}
	if len(note) > MaxPlanNote {
		return fmt.Errorf("plan note exceeds %d characters", MaxPlanNote)
	}
	ids, active := map[string]bool{}, 0
	for _, it := range items {
		if strings.TrimSpace(it.ID) == "" || len(it.ID) > 64 {
			return errors.New("every plan item needs a non-empty id of at most 64 characters")
		}
		if ids[it.ID] {
			return fmt.Errorf("duplicate plan item id %q", it.ID)
		}
		ids[it.ID] = true
		if strings.TrimSpace(it.Text) == "" || len(it.Text) > MaxPlanItemText {
			return fmt.Errorf("plan item %q needs text of 1 to %d characters", it.ID, MaxPlanItemText)
		}
		switch it.Status {
		case "pending", "done", "blocked":
		case "in_progress":
			active++
		default:
			return fmt.Errorf("plan item %q has invalid status %q (want pending, in_progress, done, or blocked)", it.ID, it.Status)
		}
	}
	if active > 1 {
		return errors.New("at most one plan item may be in_progress")
	}
	return nil
}

func (t *Trajectory) validatePlan() error {
	if !t.PlanEnabled {
		if t.Plan != nil || len(t.PlanHistory) > 0 {
			return errors.New("checkpoint has a plan but the plan tool was not enabled")
		}
		return nil
	}
	if t.Plan == nil {
		return nil
	}
	if t.Plan.Version < 1 || t.Plan.UpdatedStep < 0 || t.Plan.UpdatedStep > t.Steps {
		return errors.New("checkpoint has an invalid plan")
	}
	if err := ValidatePlanItems(t.Plan.Items, t.Plan.Note); err != nil {
		return fmt.Errorf("checkpoint has an invalid plan: %w", err)
	}
	return nil
}
