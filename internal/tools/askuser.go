package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/model"
)

// AskUserToolName is the opt-in tool that ends a run with one question for
// the user. The agent loop handles its calls; the executor never runs them.
const AskUserToolName = "ask_user"

// ask_user argument limits, in characters.
const (
	MaxAskUserQuestion = 2000
	MaxAskUserOption   = 200
	MinAskUserOptions  = 2
	MaxAskUserOptions  = 6
)

// AskUserSpec describes the ask_user tool.
func AskUserSpec() model.ToolSpec {
	return model.ToolSpec{
		Type: "function",
		Function: model.FunctionSpec{
			Name: AskUserToolName,
			Description: "Stop and ask the user one question, when information that blocks the task is missing and cannot be found in the workspace. " +
				"Call it alone in its message. The run ends; the user's answer comes back later as this call's result.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string", "description": fmt.Sprintf("the one question to ask, at most %d characters", MaxAskUserQuestion)},
					"options": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": fmt.Sprintf("optional suggested answers: %d to %d distinct strings of at most %d characters; the user may still answer freely", MinAskUserOptions, MaxAskUserOptions, MaxAskUserOption),
					},
				},
				"required": []string{"question"},
			},
		},
	}
}

// ParseAskUser validates ask_user arguments. It returns the trimmed question
// and options, or the reason they are malformed.
func ParseAskUser(arguments string) (question string, options []string, reason string) {
	var args struct {
		Question *string  `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", nil, AskUserToolName + " arguments must be {\"question\": string, \"options\": [string]}"
	}
	if args.Question == nil || strings.TrimSpace(*args.Question) == "" {
		return "", nil, AskUserToolName + " needs a non-empty question"
	}
	question = strings.TrimSpace(*args.Question)
	if utf8.RuneCountInString(question) > MaxAskUserQuestion {
		return "", nil, fmt.Sprintf("%s question exceeds %d characters", AskUserToolName, MaxAskUserQuestion)
	}
	if args.Options == nil {
		return question, nil, ""
	}
	if n := len(args.Options); n < MinAskUserOptions || n > MaxAskUserOptions {
		return "", nil, fmt.Sprintf("%s options must have %d to %d entries, got %d; omit options for a free-text question", AskUserToolName, MinAskUserOptions, MaxAskUserOptions, n)
	}
	seen := map[string]bool{}
	for _, o := range args.Options {
		o = strings.TrimSpace(o)
		switch {
		case o == "":
			return "", nil, AskUserToolName + " options must be non-empty"
		case utf8.RuneCountInString(o) > MaxAskUserOption:
			return "", nil, fmt.Sprintf("%s options must be at most %d characters each", AskUserToolName, MaxAskUserOption)
		case seen[o]:
			return "", nil, fmt.Sprintf("%s options must be distinct; %q is repeated", AskUserToolName, o)
		}
		seen[o] = true
		options = append(options, o)
	}
	return question, options, ""
}
