package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MaxFallbackModels caps the fallback chain (fallback_models,
// --fallback-model).
const MaxFallbackModels = 3

// ExtraModel is the Message.Extra key naming the model spec that produced
// an assistant message. It is set only on runs with a fallback chain.
const ExtraModel = "model"

// FallbackEntry is one model of a fallback chain: its spec as given to New
// and the Model built from it.
type FallbackEntry struct {
	Spec  string
	Model Model
}

// Switch describes one move down the fallback chain.
type Switch struct {
	From     string // model spec that failed
	To       string // model spec the request is re-sent to
	Reason   string // "http_<status>" or "network"; never a response body
	Attempts int    // attempts made on From
}

type fallbackObserverKey struct{}

// WithFallbackObserver returns a context whose Fallback queries report each
// switch to fn before re-sending the request. fn runs on the requesting
// goroutine.
func WithFallbackObserver(ctx context.Context, fn func(Switch)) context.Context {
	return context.WithValue(ctx, fallbackObserverKey{}, fn)
}

// Fallback is a Model that sends each request to the active entry and,
// when that request still fails with a transient cause after the
// provider's own retries (a RetryExhaustedError), switches to the next
// entry and re-sends the same request. Other errors are returned as they
// are. After a switch, later requests stay on the model that answered for
// the rest of the invocation. Entries[0] is the primary model.
type Fallback struct {
	Entries []FallbackEntry
	active  int
}

// NewFallback returns a Fallback over primary followed by fallbacks.
func NewFallback(primary FallbackEntry, fallbacks ...FallbackEntry) *Fallback {
	return &Fallback{Entries: append([]FallbackEntry{primary}, fallbacks...)}
}

// Active returns the spec of the model that serves the next request.
func (f *Fallback) Active() string { return f.Entries[f.active].Spec }

// Query implements Model.
func (f *Fallback) Query(ctx context.Context, messages []Message, tools []ToolSpec) (Message, Cost, error) {
	var tried []string
	for {
		e := f.Entries[f.active]
		msg, cost, err := e.Model.Query(ctx, ForModel(messages, e.Spec, f.Entries[0].Spec), tools)
		if err == nil {
			if msg.Extra == nil {
				msg.Extra = map[string]any{}
			}
			msg.Extra[ExtraModel] = e.Spec
			return msg, cost, nil
		}
		if ctx.Err() != nil {
			return msg, cost, err
		}
		var re *RetryExhaustedError
		if !errors.As(err, &re) {
			if f.active == 0 && len(tried) == 0 {
				return msg, cost, err
			}
			return msg, cost, fmt.Errorf("model %s: %w", e.Spec, err)
		}
		tried = append(tried, fmt.Sprintf("%s (%s)", e.Spec, re.Reason))
		if f.active+1 >= len(f.Entries) {
			return msg, cost, fmt.Errorf("every model in the fallback chain failed: %s: %w", strings.Join(tried, ", "), err)
		}
		next := f.Entries[f.active+1]
		if fn, ok := ctx.Value(fallbackObserverKey{}).(func(Switch)); ok && fn != nil {
			fn(Switch{From: e.Spec, To: next.Spec, Reason: re.Reason, Attempts: re.Attempts})
		}
		f.active++
	}
}

// ForModel returns messages as they may be sent to spec: provider-specific
// reasoning state (Anthropic thinking blocks, Responses/Codex reasoning
// items) is kept only on assistant messages produced by the same provider.
// A message without an ExtraModel tag counts as produced by primary. The
// input is not modified; messages without foreign state are shared.
func ForModel(messages []Message, spec, primary string) []Message {
	var out []Message
	for i, m := range messages {
		if m.Role != "assistant" || len(m.Extra) == 0 {
			continue
		}
		from, _ := m.Extra[ExtraModel].(string)
		if from == "" {
			from = primary
		}
		if SameProvider(from, spec) {
			continue
		}
		_, a := m.Extra[ExtraAnthropicContent]
		_, c := m.Extra[ExtraCodexReasoning]
		if !a && !c {
			continue
		}
		if out == nil {
			out = append([]Message(nil), messages...)
		}
		extra := make(map[string]any, len(m.Extra))
		for k, v := range m.Extra {
			if k != ExtraAnthropicContent && k != ExtraCodexReasoning {
				extra[k] = v
			}
		}
		out[i].Extra = extra
	}
	if out == nil {
		return messages
	}
	return out
}

// SameProvider reports whether two model specs use the same provider; a
// spec without a provider prefix is an openai model.
func SameProvider(a, b string) bool {
	pa, _ := splitSpec(a)
	pb, _ := splitSpec(b)
	return pa == pb
}

// IsRetryExhausted reports whether err is a request that still failed with
// a transient cause after every retry.
func IsRetryExhausted(err error) bool {
	var re *RetryExhaustedError
	return errors.As(err, &re)
}
