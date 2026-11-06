package tools

import (
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
)

// NormalizeScopedTools parses a list of tool-name tokens (from --tools,
// config tools:, or a delegate/worker tools argument). Each token may be a
// single name or a comma-separated list. Empty names and duplicates are
// rejected. "*" alone means "clear scoping" (nil). An empty input is nil.
func NormalizeScopedTools(tokens []string) ([]string, error) {
	if len(tokens) == 1 && strings.TrimSpace(tokens[0]) == "*" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, tok := range tokens {
		for _, part := range strings.Split(tok, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				return nil, fmt.Errorf("empty tool name")
			}
			if name == "*" {
				return nil, fmt.Errorf("%q clears tool scoping only when it is the sole value", name)
			}
			if seen[name] {
				return nil, fmt.Errorf("duplicate tool %q", name)
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ValidateScopedTools checks that every name in only is offered by sel.
// It returns a clear error naming the first missing tool.
func ValidateScopedTools(only []string, sel Selection) error {
	for _, name := range only {
		if !sel.Offers(name) {
			return fmt.Errorf("tool %q is not available in this run", name)
		}
	}
	return nil
}

// FilterScoped returns the subset of specs whose names are in only.
// When only is empty it returns specs unchanged.
func FilterScoped(specs []model.ToolSpec, only []string) []model.ToolSpec {
	if len(only) == 0 {
		return specs
	}
	allow := map[string]bool{}
	for _, n := range only {
		allow[n] = true
	}
	out := specs[:0:0]
	for _, s := range specs {
		if allow[s.Function.Name] {
			out = append(out, s)
		}
	}
	return out
}

// WithToolsParam adds the optional tools array argument to a delegate or
// worker spec so a call can further restrict the child's catalog. The
// fingerprint strips this property so existing checkpoint signatures stay
// stable (see Selection.fingerprintSpecs).
func WithToolsParam(s model.ToolSpec) model.ToolSpec {
	params := map[string]any{}
	for k, v := range s.Function.Parameters {
		params[k] = v
	}
	props := map[string]any{}
	if p, ok := params["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	props["tools"] = map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": "optional subset of tool names this child may use; omit or pass [] for the child's full catalog",
	}
	params["properties"] = props
	s.Function.Parameters = params
	return s
}

// EqualScopedTools reports whether a and b name the same scoped set
// (order-sensitive, matching how checkpoints record ScopedTools).
func EqualScopedTools(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
