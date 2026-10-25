// Package config loads agent settings from an optional YAML file layered over defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/vicgupta/tinyagent/embed"
)

type AgentConfig struct {
	ReadOnly bool `yaml:"read_only"`
	// Sandbox confines bash and start_process commands at the OS level:
	// writes only under --cwd, a per-run temp dir and SandboxWritablePaths.
	Sandbox bool `yaml:"sandbox"`
	// SandboxNetwork lets sandboxed commands use the network.
	SandboxNetwork bool `yaml:"sandbox_network"`
	// SandboxWritablePaths are extra absolute directories commands may write.
	SandboxWritablePaths []string `yaml:"sandbox_writable_paths,omitempty"`
	// Model is the provider/model used when neither -m nor $AGENT_MODEL sets
	// one. "" falls back to the built-in default.
	Model string `yaml:"model"`

	SystemPrompt string  `yaml:"system_prompt"`
	StepLimit    int     `yaml:"step_limit"`
	CostLimit    float64 `yaml:"cost_limit"`
	WallTimeSec  int     `yaml:"wall_time_seconds"`
	// MaxFormatErrors ends the run after this many consecutive steps whose
	// tool calls are all malformed. Must be >= 1; default 3.
	MaxFormatErrors int `yaml:"max_format_errors"`

	BashTimeoutSec int `yaml:"bash_timeout_seconds"`
	MaxOutputChars int `yaml:"max_output_chars"`
	// MaxBackgroundProcesses bounds how many start_process processes may run
	// at once.
	MaxBackgroundProcesses int `yaml:"max_background_processes"`

	// Optional price override in USD per million tokens, for models
	// missing from the built-in price table.
	InputPricePerMTok  float64 `yaml:"input_price_per_mtok"`
	OutputPricePerMTok float64 `yaml:"output_price_per_mtok"`

	// ContextWindow, in tokens, overrides the built-in context window table.
	// 0 uses the table.
	ContextWindow int `yaml:"context_window"`

	MCPServers []MCPServer `yaml:"mcp_servers,omitempty"`

	// Hooks are trusted local executables run around matching tool calls.
	Hooks Hooks `yaml:"hooks,omitempty"`

	// Plan configures the opt-in update_plan tool (off by default).
	Plan Plan `yaml:"plan,omitempty"`
	// LogDB is the opt-in SQLite event log.
	LogDB LogDB `yaml:"log_db"`
}

// Plan configures the persistent plan tool.
type Plan struct {
	Enabled bool `yaml:"enabled"`
}

// LogDB configures the opt-in SQLite event log. It is off unless Enabled
// (or --log-db) is set.
type LogDB struct {
	Enabled bool `yaml:"enabled"`
	// Path is the database file; "" means .tinyagent/tinyagent.db under
	// --cwd. A relative path is resolved against --cwd; "~/" is expanded.
	Path string `yaml:"path"`
	// Level is "metadata" (default; no prompt, message, argument or output
	// bodies) or "full".
	Level string `yaml:"level"`
	// MaxDays and MaxRuns prune older runs at run start; 0 keeps all.
	MaxDays int `yaml:"max_days"`
	MaxRuns int `yaml:"max_runs"`
	// MaxPayloadBytes caps each stored event payload; 0 means 65536.
	MaxPayloadBytes int `yaml:"max_payload_bytes"`
}

// Hooks lists the commands run before and after matching tool calls.
type Hooks struct {
	PreTool  []Hook `yaml:"pre_tool,omitempty"`
	PostTool []Hook `yaml:"post_tool,omitempty"`
}

// Hook runs one local command for tool calls whose name matches Match.
type Hook struct {
	Match   string   `yaml:"match,omitempty"` // whole-name Go regexp; "" matches every tool
	Command string   `yaml:"command"`
	Args    []string `yaml:"args,omitempty"`
	Env     []string `yaml:"env,omitempty"`
	// TimeoutSec is nil when unset (30 seconds); 0 is rejected.
	TimeoutSec *int   `yaml:"timeout_seconds,omitempty"`
	OnError    string `yaml:"on_error,omitempty"` // pre_tool only: deny (default) or allow
}

// MCPServer grants access to selected tools from one local stdio server.
type MCPServer struct {
	Alias               string   `yaml:"alias"`
	Command             string   `yaml:"command"`
	Args                []string `yaml:"args,omitempty"`
	Tools               []string `yaml:"tools"`
	Env                 []string `yaml:"env,omitempty"`
	DiscoveryTimeoutSec int      `yaml:"discovery_timeout_seconds"`
	CallTimeoutSec      int      `yaml:"call_timeout_seconds"`
}

// DefaultMaxFormatErrors is the default MaxFormatErrors.
const DefaultMaxFormatErrors = 3

func Default() AgentConfig {
	return AgentConfig{
		SystemPrompt:   embed.SystemPrompt(),
		BashTimeoutSec: 120,
		MaxOutputChars: 30000,

		MaxBackgroundProcesses: 4,
		MaxFormatErrors:        DefaultMaxFormatErrors,
		LogDB:                  LogDB{Level: "metadata", MaxDays: 30, MaxRuns: 500},
	}
}

// DefaultPath returns the per-user config file, tinyagent/config.yaml under
// os.UserConfigDir, whether or not it exists.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tinyagent", "config.yaml"), nil
}

// Find returns the config file to Load: explicit if set, else the per-user
// config if it exists, else "" for the defaults. A user config that exists
// but can't be read is still returned, so Load reports it by name.
func Find(explicit string) string {
	if explicit != "" {
		return explicit
	}
	p, err := DefaultPath()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return p
}

// Load returns the defaults overlaid with the YAML file at path. An empty path
// returns the defaults. Unknown keys are an error, so a typo can't silently
// drop a setting.
func Load(path string) (AgentConfig, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	var probe struct {
		MaxFormatErrors yaml.Node `yaml:"max_format_errors"`
	}
	if yaml.Unmarshal(data, &probe) == nil && probe.MaxFormatErrors.Kind != 0 {
		if n := &probe.MaxFormatErrors; n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" {
			return cfg, fmt.Errorf("config %s: max_format_errors must be a whole number >= 1, got %q", path, n.Value)
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) { // io.EOF: empty or comment-only file
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if l := cfg.LogDB.Level; l != "metadata" && l != "full" {
		return cfg, fmt.Errorf("config %s: log_db.level %q must be metadata or full", path, l)
	}
	if cfg.LogDB.MaxDays < 0 || cfg.LogDB.MaxRuns < 0 || cfg.LogDB.MaxPayloadBytes < 0 {
		return cfg, fmt.Errorf("config %s: log_db max_days, max_runs and max_payload_bytes must be >= 0", path)
	}
	if cfg.MaxFormatErrors < 1 {
		return cfg, fmt.Errorf("config %s: max_format_errors must be a whole number >= 1, got %d", path, cfg.MaxFormatErrors)
	}
	for _, p := range cfg.SandboxWritablePaths {
		if !filepath.IsAbs(p) {
			return cfg, fmt.Errorf("config %s: sandbox_writable_paths entry %q must be an absolute path", path, p)
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return cfg, fmt.Errorf("config %s: sandbox_writable_paths entry %q is not an existing directory", path, p)
		}
	}
	return cfg, nil
}
