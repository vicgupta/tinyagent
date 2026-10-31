package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
)

// checkSummary is the --check summary (issue #292): the compact table at
// the top of --check and, with --json, the whole output. Its JSON shape is
// documented in docs/json-output.md; bump checkSchemaVersion on any
// incompatible change. It never holds a secret value: an API key is only
// "set", "missing" or "n/a".
type checkSummary struct {
	SchemaVersion int              `json:"schema_version"`
	OK            bool             `json:"ok"`
	Config        checkConfig      `json:"config"`
	Profile       string           `json:"profile"`
	Model         checkModel       `json:"model"`
	Provider      checkProvider    `json:"provider"`
	Fallbacks     []string         `json:"fallbacks"`
	Sandbox       checkSandboxInfo `json:"sandbox"`
	Permissions   checkPermissions `json:"permissions"`
	Lockdown      checkLockdownSum `json:"lockdown"`
	Cwd           string           `json:"cwd"`
	Limits        checkLimits      `json:"limits"`
	Error         *checkError      `json:"error"`
}

const checkSchemaVersion = 1

type checkConfig struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

type checkModel struct {
	Spec   string `json:"spec"`
	Source string `json:"source"`
}

type checkProvider struct {
	Name      string `json:"name"`
	APIKeyEnv string `json:"api_key_env"`
	APIKey    string `json:"api_key"`
}

type checkSandboxInfo struct {
	Enabled   bool   `json:"enabled"`
	Backend   string `json:"backend"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

type checkPermissions struct {
	Configured     bool   `json:"configured"`
	Rules          int    `json:"rules"`
	Default        string `json:"default"`
	NonInteractive string `json:"non_interactive"`
}

type checkLockdownSum struct {
	Enabled bool   `json:"enabled"`
	Detail  string `json:"detail"`
}

type checkLimits struct {
	Steps   int     `json:"steps"`
	CostUSD float64 `json:"cost_usd"`
	Tokens  int     `json:"tokens"`
	TimeSec int     `json:"time_sec"`
}

type checkError struct {
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func newCheckSummary() *checkSummary {
	return &checkSummary{SchemaVersion: checkSchemaVersion, Fallbacks: []string{}}
}

// setConfigLine fills Config from the --check config line, "<path>
// (<source>)" or "none (<source>)", so the summary follows whatever label
// the config line uses.
func (s *checkSummary) setConfigLine(v string) {
	path, src := v, ""
	if i := strings.LastIndex(v, " ("); i >= 0 && strings.HasSuffix(v, ")") {
		path, src = v[:i], v[i+2:len(v)-1]
	}
	if path == "none" {
		path = ""
	}
	s.Config = checkConfig{Path: path, Source: strings.TrimPrefix(src, "from ")}
}

// setProvider fills Provider for spec: the provider name, the API key
// variable it reads (if any) and whether that variable is set.
func (s *checkSummary) setProvider(spec string) {
	env := ""
	name, _, ok := strings.Cut(spec, "/")
	if !ok {
		name = "openai"
	}
	if d, ok := model.ConfiguredProvider(spec); ok {
		name, env = d.ID, d.APIKeyEnv
	} else {
		env = map[string]string{"openai": "OPENAI_API_KEY", "openrouter": "OPENROUTER_API_KEY", "anthropic": "ANTHROPIC_API_KEY"}[name]
	}
	s.Provider = checkProvider{Name: name, APIKeyEnv: env, APIKey: "n/a"}
	if env != "" {
		s.Provider.APIKey = "set"
		if os.Getenv(env) == "" {
			s.Provider.APIKey = "missing"
		}
	}
}

// writeTable prints the summary table that heads --check's text output.
func (s *checkSummary) writeTable(w io.Writer) {
	key := "n/a"
	if s.Provider.APIKeyEnv != "" {
		key = s.Provider.APIKeyEnv + " " + s.Provider.APIKey
	}
	sandbox := "off"
	if s.Sandbox.Enabled {
		sandbox = s.Sandbox.Detail
	}
	perms := "none configured"
	if s.Permissions.Configured {
		perms = fmt.Sprintf("%d rules, default %s, non-interactive %s", s.Permissions.Rules, s.Permissions.Default, s.Permissions.NonInteractive)
	}
	lock := "off"
	if s.Lockdown.Enabled {
		lock = "on"
	}
	cfg := s.Config.Path
	if cfg == "" {
		cfg = "none"
	}
	if s.Config.Source != "" {
		cfg += " (" + s.Config.Source + ")"
	}
	fmt.Fprintln(w, "summary")
	for _, r := range [][2]string{
		{"model", s.Model.Spec},
		{"provider", s.Provider.Name},
		{"api key", key},
		{"sandbox", sandbox},
		{"permissions", perms},
		{"lockdown", lock},
		{"config", cfg},
	} {
		fmt.Fprintf(w, "  %-12s %s\n", r[0], r[1])
	}
	fmt.Fprintln(w)
}

// writeJSON prints the summary as one JSON object.
func (s *checkSummary) writeJSON(w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(s)
}
