package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message.Extra keys for output-token truncation. A provider sets them when
// its documented stop signal says the response was cut off at the output
// limit, so the agent loop reads one flag whatever the provider.
const (
	// ExtraTruncated is true on a response cut off at the output-token limit.
	ExtraTruncated = "truncated"
	// ExtraTruncationReason is the provider's raw reason, for logs.
	ExtraTruncationReason = "truncation_reason"
)

// DefaultAnthropicMaxTokens is the max_tokens sent to Anthropic when
// max_output_tokens is unset.
const DefaultAnthropicMaxTokens = 8192

// Truncated reports whether m was cut off at the output-token limit, and the
// provider's raw reason.
func Truncated(m Message) (bool, string) {
	on, _ := m.Extra[ExtraTruncated].(bool)
	if !on {
		return false, ""
	}
	reason, _ := m.Extra[ExtraTruncationReason].(string)
	return true, reason
}

// markTruncated records a truncation on m.
func markTruncated(m *Message, reason string) {
	if m.Extra == nil {
		m.Extra = map[string]any{}
	}
	m.Extra[ExtraTruncated] = true
	m.Extra[ExtraTruncationReason] = reason
}

// anthropicTruncated reports whether an Anthropic stop_reason means the
// response was cut off.
func anthropicTruncated(stopReason string) bool {
	return stopReason == "max_tokens" || stopReason == "model_context_window_exceeded"
}

// responsesTruncated reports whether a Responses API state (see
// responsesObject.state) means the response hit the output-token limit.
func responsesTruncated(state string) bool {
	return state == "incomplete: max_output_tokens"
}

// MaxOutputTokensSupported reports whether the provider of spec accepts a
// per-request output-token budget, and returns an error naming it if not.
func MaxOutputTokensSupported(spec string) error {
	provider, _ := splitSpec(spec)
	switch provider {
	case "anthropic", "openai", "openrouter", "ollama":
		return nil
	}
	return fmt.Errorf("max_output_tokens is not supported by the %s provider", provider)
}

// SetMaxOutputTokens configures m, built by New(spec, ...), to send n as its
// provider's per-request output-token limit. n == 0 leaves m unchanged (the
// provider default; 8192 for Anthropic).
func SetMaxOutputTokens(m Model, spec string, n int) error {
	if n < 0 {
		return fmt.Errorf("invalid max_output_tokens %d (want a whole number >= 0)", n)
	}
	if n == 0 {
		return nil
	}
	if err := MaxOutputTokensSupported(spec); err != nil {
		return err
	}
	provider, _ := splitSpec(spec)
	switch p := m.(type) {
	case *Anthropic:
		p.MaxTokens = n
	case *OpenAIResponses:
		p.MaxOutputTokens = n
	case *OpenAI:
		p.MaxOutputTokens = n
		p.MaxOutputKey = "max_completion_tokens"
		if provider == "ollama" {
			p.MaxOutputKey = "max_tokens"
		}
	default:
		return fmt.Errorf("max_output_tokens is not supported by the %s provider", strings.TrimSpace(provider))
	}
	return nil
}

// replayArguments returns the arguments to send back for tc of m: as stored,
// except that a cut-off call's incomplete JSON is replaced by {} so the
// provider accepts the history.
func replayArguments(m Message, tc ToolCall) string {
	if cut, _ := Truncated(m); cut && !json.Valid([]byte(tc.Function.Arguments)) {
		return "{}"
	}
	return tc.Function.Arguments
}
