package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// specList is the repeatable --fallback-model flag.
type specList []string

func (l *specList) String() string { return strings.Join(*l, ",") }
func (l *specList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("empty model spec (use --no-fallback to turn fallback off)")
	}
	*l = append(*l, v)
	return nil
}

// fallbackModels returns this invocation's fallback chain: --no-fallback
// clears it, --fallback-model (repeatable) replaces fallback_models.
func fallbackModels(fs *flag.FlagSet, flagSpecs []string, noFallback bool, cfg config.AgentConfig) ([]string, error) {
	if noFallback {
		if flagWasSet(fs, "fallback-model") {
			return nil, fmt.Errorf("--no-fallback conflicts with --fallback-model")
		}
		return nil, nil
	}
	if flagWasSet(fs, "fallback-model") {
		return append([]string(nil), flagSpecs...), nil
	}
	return append([]string(nil), cfg.FallbackModels...), nil
}

// checkFallbacks validates a fallback chain for primary before any
// request: each entry must be a usable model spec of a known provider with
// its credentials set, different from primary and every earlier entry,
// and the chain at most model.MaxFallbackModels long.
func checkFallbacks(primary string, specs []string, openaiAPI string) error {
	if len(specs) > model.MaxFallbackModels {
		return fmt.Errorf("fallback_models/--fallback-model: at most %d fallback models are allowed, got %d", model.MaxFallbackModels, len(specs))
	}
	seen := map[string]bool{primary: true}
	for _, spec := range specs {
		if strings.TrimSpace(spec) != spec || spec == "" {
			return fmt.Errorf("fallback model %q: must be a non-empty provider/model spec", spec)
		}
		if spec == primary {
			return fmt.Errorf("fallback model %q is the primary model", spec)
		}
		if seen[spec] {
			return fmt.Errorf("fallback model %q is listed more than once", spec)
		}
		seen[spec] = true
		if err := model.CheckProviderEnv(spec); err != nil {
			return fmt.Errorf("fallback model %q: %w", spec, err)
		}
		if env := model.MissingKeyEnv(spec); env != "" {
			return fmt.Errorf("fallback model %q: %s is not set", spec, env)
		}
		if _, err := model.NewWith(spec, nil, model.Options{OpenAIAPI: model.OpenAITransport(spec, openaiAPI)}); err != nil {
			return fmt.Errorf("fallback model %q: %w", spec, err)
		}
	}
	return nil
}

// withFallbacks returns primary (spec primarySpec) wrapped in a
// *model.Fallback over specs, or primary itself when specs is empty. Each
// fallback streams like the primary; reasoning effort and the output-token
// budget carry over only to a fallback on the primary's provider, and
// others keep their defaults. Unpriced fallbacks get the cost-limit
// warning.
func withFallbacks(primary model.Model, primarySpec string, specs []string, onText func(string), cfg config.AgentConfig, effort string, warn io.Writer) (model.Model, error) {
	if len(specs) == 0 {
		return primary, nil
	}
	var entries []model.FallbackEntry
	for _, spec := range specs {
		m, err := model.NewWith(spec, onText, model.Options{OpenAIAPI: model.OpenAITransport(spec, cfg.OpenAIAPI)})
		if err != nil {
			return nil, fmt.Errorf("fallback model %q: %w", spec, err)
		}
		if sameProvider(spec, primarySpec) {
			if err := model.SetReasoningEffort(m, spec, effort); err != nil {
				return nil, fmt.Errorf("fallback model %q: %w", spec, err)
			}
			if err := model.SetMaxOutputTokens(m, spec, cfg.MaxOutputTokens); err != nil {
				return nil, fmt.Errorf("fallback model %q: %w", spec, err)
			}
		}
		warnUnpriced(warn, spec, cfg.CostLimit)
		entries = append(entries, model.FallbackEntry{Spec: spec, Model: m})
	}
	return model.NewFallback(model.FallbackEntry{Spec: primarySpec, Model: primary}, entries...), nil
}
