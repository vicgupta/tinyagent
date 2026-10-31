package model

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
)

// Wire protocols a configured provider can speak.
const (
	ProviderAPIChat      = "chat"
	ProviderAPIResponses = "responses"
	ProviderAPIAnthropic = "anthropic"
)

// builtinProviders are the provider names New knows without configuration.
var builtinProviders = []string{"openai", "openrouter", "anthropic", "ollama", "codex", "claude-code", "codex-exec", "opencode"}

// ModelMeta is per-model price and context-window metadata for a
// configured provider. Zero values mean unknown.
type ModelMeta struct {
	InputPricePerMTok   float64
	OutputPricePerMTok  float64
	CacheReadMultiplier float64 // 0 means the standard 0.1
	ContextWindow       int
}

func (m ModelMeta) priced() bool { return m.InputPricePerMTok != 0 || m.OutputPricePerMTok != 0 }

// ProviderDef is a config-defined provider. APIKeyEnv and EnvHeaders name
// environment variables; their values are read only when a model is built.
type ProviderDef struct {
	ID         string
	API        string
	BaseURL    string
	APIKeyEnv  string
	Headers    map[string]string
	EnvHeaders map[string]string
	Models     map[string]ModelMeta
}

// configured holds the providers set by SetProviders, by id.
var configured = map[string]ProviderDef{}

// SetProviders replaces the configured providers. Definitions are assumed
// validated (see config.Load).
func SetProviders(defs []ProviderDef) {
	configured = make(map[string]ProviderDef, len(defs))
	for _, d := range defs {
		configured[d.ID] = d
	}
}

// ConfiguredProvider returns the configured provider spec resolves to, if
// any. Built-in names always resolve to the built-in provider.
func ConfiguredProvider(spec string) (ProviderDef, bool) {
	provider, _ := splitSpec(spec)
	if slices.Contains(builtinProviders, provider) {
		return ProviderDef{}, false
	}
	d, ok := configured[provider]
	return d, ok
}

// configuredMeta returns the metadata for spec's model, matched by exact
// name, when spec names a configured provider.
func configuredMeta(spec string) (ModelMeta, bool) {
	d, ok := ConfiguredProvider(spec)
	if !ok {
		return ModelMeta{}, false
	}
	_, name := splitSpec(spec)
	m, ok := d.Models[name]
	return m, ok
}

// providerIDs lists the built-in and configured provider ids for errors.
func providerIDs() string {
	ids := append([]string(nil), builtinProviders...)
	var extra []string
	for id := range configured {
		extra = append(extra, id)
	}
	sort.Strings(extra)
	s := strings.Join(ids[:len(ids)-1], ", ") + ", or " + ids[len(ids)-1]
	if len(extra) > 0 {
		s = strings.Join(ids, ", ") + "; configured: " + strings.Join(extra, ", ")
	}
	return s
}

// ProviderEnvError reports a missing environment variable a configured
// provider needs. It names the variable, never a value.
type ProviderEnvError struct {
	Provider, Env, Use string
}

func (e *ProviderEnvError) Error() string {
	return fmt.Sprintf("provider %q: %s %s is unset or empty", e.Provider, e.Use, e.Env)
}

// CheckProviderEnv reports the first variable that the configured provider
// of spec needs but that is unset or empty, or nil (also for built-in
// providers).
func CheckProviderEnv(spec string) error {
	d, ok := ConfiguredProvider(spec)
	if !ok {
		return nil
	}
	_, err := providerHeaders(d)
	return err
}

// providerHeaders resolves d's static and env-sourced headers.
func providerHeaders(d ProviderDef) (map[string]string, error) {
	if d.APIKeyEnv != "" && os.Getenv(d.APIKeyEnv) == "" {
		return nil, &ProviderEnvError{Provider: d.ID, Env: d.APIKeyEnv, Use: "api_key_env"}
	}
	h := make(map[string]string, len(d.Headers)+len(d.EnvHeaders))
	for k, v := range d.Headers {
		h[k] = v
	}
	names := make([]string, 0, len(d.EnvHeaders))
	for k := range d.EnvHeaders {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		env := d.EnvHeaders[k]
		v := os.Getenv(env)
		if v == "" {
			return nil, &ProviderEnvError{Provider: d.ID, Env: env, Use: fmt.Sprintf("env_headers %s:", k)}
		}
		h[k] = v
	}
	return h, nil
}

// OnCredentials, when set, receives credential values a provider loads
// from somewhere other than the environment, such as the codex provider's
// tokens (including ones refreshed during the run), for redaction.
var OnCredentials func(values ...string)

func credentialsLoaded(values ...string) {
	if OnCredentials != nil {
		OnCredentials(values...)
	}
}

// InsecureBaseURL reports whether d sends requests over plain http to a
// host other than loopback.
func InsecureBaseURL(d ProviderDef) bool {
	u, err := url.Parse(d.BaseURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// newConfigured builds the adapter for configured provider d.
func newConfigured(d ProviderDef, spec, name string, onText func(string)) (Model, error) {
	headers, err := providerHeaders(d)
	if err != nil {
		return nil, err
	}
	key := ""
	if d.APIKeyEnv != "" {
		key = os.Getenv(d.APIKeyEnv)
	}
	price := providerPrice(spec)
	switch d.API {
	case ProviderAPIChat:
		return &OpenAI{BaseURL: d.BaseURL, APIKey: key, Model: name, OnText: onText, Headers: headers, Price: price}, nil
	case ProviderAPIResponses:
		return &OpenAIResponses{BaseURL: d.BaseURL, APIKey: key, Model: name, OnText: onText, Headers: headers, Price: price}, nil
	case ProviderAPIAnthropic:
		return &Anthropic{BaseURL: d.BaseURL, APIKey: key, Model: name, OnText: onText, Headers: headers, Price: price, OmitEmptyKey: true}, nil
	}
	return nil, fmt.Errorf("provider %q: unknown api %q", d.ID, d.API)
}

// providerPrice returns the configured price entry for spec, in Prices
// form, or nil when the model has no configured price.
func providerPrice(spec string) *[3]float64 {
	m, ok := configuredMeta(spec)
	if !ok {
		return nil
	}
	if m.priced() {
		return &[3]float64{m.InputPricePerMTok, m.OutputPricePerMTok, m.CacheReadMultiplier}
	}
	// A multiplier alone refines the built-in price for the model name.
	_, name := splitSpec(spec)
	if p, ok := lookupPrice(name); ok && m.CacheReadMultiplier > 0 {
		p[2] = m.CacheReadMultiplier
		return &p
	}
	return nil
}
