package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// loadConfigError reports a config load failure. Invalid provider entries
// and other invalid settings exit 2, like usage errors caught before any
// request.
func loadConfigError(err error) int {
	if errors.Is(err, config.ErrInvalidProvider) {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return configFail(err)
}

// registerProviders hands cfg's providers to the model package so that
// -m <id>/<model> resolves them.
func registerProviders(cfg config.AgentConfig) {
	defs := make([]model.ProviderDef, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		d := model.ProviderDef{
			ID: p.ID, API: p.API, BaseURL: p.BaseURL, APIKeyEnv: p.APIKeyEnv,
			Headers: p.Headers, EnvHeaders: p.EnvHeaders,
		}
		if len(p.Models) > 0 {
			d.Models = make(map[string]model.ModelMeta, len(p.Models))
			for name, m := range p.Models {
				d.Models[name] = model.ModelMeta{
					InputPricePerMTok:   m.InputPricePerMTok,
					OutputPricePerMTok:  m.OutputPricePerMTok,
					CacheReadMultiplier: m.CacheReadMultiplier,
					ContextWindow:       m.ContextWindow,
				}
			}
		}
		defs = append(defs, d)
	}
	model.SetProviders(defs)
}

// providerInfo returns the api and base_url of spec's configured provider,
// or empty strings for a built-in provider.
func providerInfo(spec string) (api, baseURL string) {
	if d, ok := model.ConfiguredProvider(spec); ok {
		return d.API, d.BaseURL
	}
	return "", ""
}

// announceProvider prints the -v line and the plain-http warning for a
// configured provider. Neither includes a key or header value.
func announceProvider(w io.Writer, spec string, verbose bool) {
	d, ok := model.ConfiguredProvider(spec)
	if !ok {
		return
	}
	if verbose {
		fmt.Fprintf(w, "provider: %s (api %s, base_url %s)\n", d.ID, d.API, d.BaseURL)
	}
	if model.InsecureBaseURL(d) {
		fmt.Fprintf(w, "warning: provider %q uses plain http to a non-loopback host (%s); requests, including any key, are sent unencrypted\n", d.ID, d.BaseURL)
	}
}

// checkResumeProvider refuses to resume a checkpoint from a configured
// provider whose entry is missing or whose api or base_url changed.
func checkResumeProvider(src *trajectory.Trajectory, spec string) error {
	d, ok := model.ConfiguredProvider(spec)
	if !ok {
		if src.ProviderAPI != "" {
			provider, _, _ := strings.Cut(spec, "/")
			return fmt.Errorf("resume: the checkpoint used configured provider %q, which is not defined in the current config's providers; restore the entry or start a fresh task", provider)
		}
		return nil
	}
	if src.ProviderAPI != "" && d.API != src.ProviderAPI {
		return fmt.Errorf("resume: provider %q api changed from %q to %q since the checkpoint; the saved history only replays on the original api, so start a fresh task", d.ID, src.ProviderAPI, d.API)
	}
	if src.ProviderBaseURL != "" && d.BaseURL != src.ProviderBaseURL {
		return fmt.Errorf("resume: provider %q base_url changed from %s to %s since the checkpoint; restore it or start a fresh task", d.ID, src.ProviderBaseURL, d.BaseURL)
	}
	return nil
}
