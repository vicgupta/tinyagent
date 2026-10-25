package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message.Extra keys holding provider reasoning state. They are written by
// the provider that produced the message and ignored by every other one.
const (
	// ExtraAnthropicContent holds every content block of an Anthropic
	// response that contained thinking, in order, for verbatim replay.
	ExtraAnthropicContent = "anthropic_content"
	// ExtraCodexReasoning holds the Responses API reasoning output items
	// (with encrypted_content), in order, for replay before the message.
	ExtraCodexReasoning = "codex_reasoning"
)

// ReasoningEfforts are the accepted reasoning_effort values. Support varies
// by model; unsupported values are reported by the provider.
var ReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ValidReasoningEffort reports whether v is an accepted reasoning_effort.
func ValidReasoningEffort(v string) bool {
	for _, e := range ReasoningEfforts {
		if v == e {
			return true
		}
	}
	return false
}

// SetReasoningEffort configures m, built by New(spec, ...), to send effort
// using its provider's native control. An empty effort leaves m unchanged.
func SetReasoningEffort(m Model, spec, effort string) error {
	if effort == "" {
		return nil
	}
	if !ValidReasoningEffort(effort) {
		return fmt.Errorf("invalid reasoning_effort %q (want one of %s)", effort, strings.Join(ReasoningEfforts, ", "))
	}
	provider, _ := splitSpec(spec)
	switch p := m.(type) {
	case *Anthropic:
		p.ReasoningEffort = effort
	case *Codex:
		p.ReasoningEffort = effort
	case *OpenAI:
		switch provider {
		case "openai":
			p.ReasoningEffort = effort
		case "openrouter":
			p.ReasoningEffort, p.NestedReasoning = effort, true
		default:
			return fmt.Errorf("reasoning_effort is not supported by the %s provider", provider)
		}
	default:
		return fmt.Errorf("reasoning_effort is not supported by the %s provider", provider)
	}
	return nil
}

// ReasoningChars approximates the size of the reasoning state stored in m,
// which is sent back with the history.
func ReasoningChars(m Message) int {
	n := 0
	for _, k := range []string{ExtraAnthropicContent, ExtraCodexReasoning} {
		if v, ok := m.Extra[k]; ok {
			b, _ := json.Marshal(v)
			n += len(b)
		}
	}
	return n
}

// rawItems decodes a stored Extra value (raw JSON from this run, or generic
// JSON from a trajectory) into raw JSON items.
func rawItems(v any) ([]json.RawMessage, bool) {
	if v == nil {
		return nil, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(b, &items) != nil || len(items) == 0 {
		return nil, false
	}
	return items, true
}
