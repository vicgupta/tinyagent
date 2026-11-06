package agent

import (
	"fmt"

	"github.com/vicgupta/tinyagent/internal/tools"
)

// scopedChildTools validates an optional per-call tools list against the
// child's allowed catalog. An empty list means no further restriction (nil).
func scopedChildTools(want []string, sel tools.Selection) ([]string, error) {
	if len(want) == 0 {
		return nil, nil
	}
	only, err := tools.NormalizeScopedTools(want)
	if err != nil {
		return nil, err
	}
	if err := tools.ValidateScopedTools(only, sel); err != nil {
		return nil, err
	}
	return only, nil
}

// intersectScopedTools validates want against allowed and returns the
// intersection in want order (after normalization).
func intersectScopedTools(want, allowed []string) ([]string, error) {
	only, err := tools.NormalizeScopedTools(want)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range allowed {
		have[n] = true
	}
	var out []string
	for _, n := range only {
		if !have[n] {
			return nil, fmt.Errorf("tool %q is not available to this worker", n)
		}
		out = append(out, n)
	}
	return out, nil
}
