package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/model"
)

// RememberToolName is the opt-in project memory tool (memory.enabled). It
// saves short notes about the project for later runs; writes never change
// the current run's prompt.
const RememberToolName = "remember"

// RememberSpec describes the remember tool with the default entry size.
func RememberSpec() model.ToolSpec { return RememberSpecFor(memory.MaxTextLen) }

// RememberSpecFor describes the remember tool for entries of at most max
// characters (memory.toml max_entry_size).
func RememberSpecFor(max int) model.ToolSpec {
	if max <= 0 {
		max = memory.MaxTextLen
	}
	return model.ToolSpec{
		Type: "function",
		Function: model.FunctionSpec{
			Name: RememberToolName,
			Description: "Save, replace or forget a short, durable note about this project for FUTURE runs (a build quirk, the test command " +
				"that works, a convention reviewers enforce). Notes never appear in this run's prompt. Keep each note to one fact, " +
				fmt.Sprintf("at most %d characters, with no secrets. ", max) +
				"replace and forget accept only ids shown in the Project memory section or added earlier in this run. " +
				"A note may be held as pending until a person approves it.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"op":   map[string]any{"type": "string", "enum": []string{"add", "replace", "forget"}, "description": "add a new note, replace an existing note's text, or forget a note"},
					"text": map[string]any{"type": "string", "description": fmt.Sprintf("the note, at most %d characters (add and replace)", max)},
					"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "optional short labels for memory_search (add only), e.g. [\"build\"]"},
					"id":   map[string]any{"type": "string", "description": "the note's id (replace and forget)"},
				},
				"required": []string{"op"},
			},
		},
	}
}

// remember runs one remember call against w.
func (e *Executor) remember(raw string) (string, error) {
	var a struct {
		Op   string   `json:"op"`
		Text string   `json:"text"`
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", errors.New(`remember arguments must be {"op": "add"|"replace"|"forget", "text": string, "id": string}`)
	}
	w := e.Remember
	if w.Path == "" {
		return "", errors.New("the project memory store is unavailable in this run (see the warning at run start); nothing was saved")
	}
	id := strings.TrimSpace(a.ID)
	var (
		res memory.Result
		err error
	)
	switch a.Op {
	case "add":
		if id != "" {
			return "", errors.New("remember add takes text only; use replace to change an existing note")
		}
		res, err = w.AddTagged(a.Text, a.Tags)
	case "replace":
		if len(a.Tags) > 0 {
			return "", errors.New("remember replace keeps the note's tags; pass tags only with add")
		}
		if id == "" {
			return "", errors.New("remember replace needs the id of the note to replace")
		}
		res, err = w.Replace(id, a.Text)
	case "forget":
		if len(a.Tags) > 0 {
			return "", errors.New("remember forget takes only an id")
		}
		if id == "" {
			return "", errors.New("remember forget needs the id of the note to forget")
		}
		res, err = w.Forget(id)
	default:
		return "", fmt.Errorf("remember op %q must be add, replace or forget", a.Op)
	}
	if err != nil {
		return "", err
	}
	return rememberResult(res, a.Op == "replace" && res.ID != id), nil
}

func rememberResult(r memory.Result, separate bool) string {
	var b strings.Builder
	switch r.Op {
	case "forget":
		fmt.Fprintf(&b, "forgot memory entry %s; later runs won't load it.", r.ID)
		return b.String()
	case "replace":
		if separate {
			fmt.Fprintf(&b, "saved replacement %s (status: %s); the current entry stays in place until a person approves the replacement.", r.ID, r.Status)
		} else {
			fmt.Fprintf(&b, "replaced memory entry %s (status: %s).", r.ID, r.Status)
		}
	default:
		fmt.Fprintf(&b, "saved memory entry %s (status: %s).", r.ID, r.Status)
	}
	if r.Status == memory.StatusPending {
		b.WriteString(" Pending entries load only after a person approves them.")
	}
	b.WriteString(" It takes effect in future runs, not this one.")
	if r.Redacted {
		b.WriteString(" Secret-looking text was replaced with [REDACTED] before saving.")
	}
	return b.String()
}
