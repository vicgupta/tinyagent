// Package config loads agent settings from an optional YAML file layered over defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

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
	// Environment selects where bash and start_process commands run:
	// "local" (the default, also "") or "container".
	Environment string `yaml:"environment"`
	// Container configures the "container" environment.
	Container Container `yaml:"container"`
	// CommandEnv is the environment policy for local bash and start_process
	// commands. The zero value inherits tinyagent's whole environment.
	CommandEnv CommandEnv `yaml:"command_env"`
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

	// ReasoningEffort is sent to the provider's reasoning control: none,
	// minimal, low, medium, high, xhigh, or max. "" sends nothing, so the
	// model's default applies.
	ReasoningEffort string `yaml:"reasoning_effort"`

	// OpenAIAPI picks the openai provider's transport: auto (the default;
	// the Responses API for api.openai.com, Chat Completions when
	// OPENAI_BASE_URL points elsewhere), responses, or chat. Other providers
	// ignore it.
	OpenAIAPI string `yaml:"openai_api"`

	MCPServers []MCPServer `yaml:"mcp_servers,omitempty"`

	// Hooks are trusted local executables run around matching tool calls.
	Hooks Hooks `yaml:"hooks,omitempty"`

	// Plan configures the opt-in update_plan tool (off by default).
	Plan Plan `yaml:"plan,omitempty"`
	// Images configures the opt-in view_image tool and MCP image
	// pass-through (off by default).
	Images Images `yaml:"images,omitempty"`
	// MaxImageBytes caps one image's raw size. Must be >= 1; default 5 MiB.
	MaxImageBytes int `yaml:"max_image_bytes"`
	// Skills turns on Agent Skills discovery from .agents/skills (off by
	// default; --skills / --no-skills override it).
	Skills bool `yaml:"skills"`
	// LogDB is the opt-in SQLite event log.
	LogDB LogDB `yaml:"log_db"`
}

// Images turns on sending images to vision-capable models.
type Images struct {
	Enabled bool `yaml:"enabled"`
}

// DefaultMaxImageBytes is the default max_image_bytes (5 MiB).
const DefaultMaxImageBytes = 5 << 20

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

// Hooks lists the commands run before and after matching tool calls, and
// the stop hooks that verify a run before it ends successfully.
type Hooks struct {
	PreTool  []Hook `yaml:"pre_tool,omitempty"`
	PostTool []Hook `yaml:"post_tool,omitempty"`
	// Stop hooks run when a run is about to end Submitted or Completed and
	// can send the model back to work. Match and OnError are rejected.
	Stop []Hook `yaml:"stop,omitempty"`
	// MaxStopContinuations caps how many times stop hooks may send the
	// model back to work in one run; nil means DefaultMaxStopContinuations.
	MaxStopContinuations *int `yaml:"max_stop_continuations,omitempty"`
}

// DefaultMaxStopContinuations is the default hooks.max_stop_continuations.
const DefaultMaxStopContinuations = 5

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

// Container is the image and runtime used by the "container" environment.
type Container struct {
	Runtime string   `yaml:"runtime"` // docker (default) or podman
	Image   string   `yaml:"image"`
	RunArgs []string `yaml:"run_args,omitempty"` // extra `run` arguments
}

// CommandEnv decides which environment variables bash and start_process
// commands receive. It is applied in order: the Inherit base, then
// ExcludeSecrets, Exclude, Pass (re-adding from tinyagent's environment) and
// Set.
type CommandEnv struct {
	Inherit        string            `yaml:"inherit"` // all (default, also ""), core or none
	ExcludeSecrets bool              `yaml:"exclude_secrets"`
	Exclude        []string          `yaml:"exclude,omitempty"` // names or * / ? globs
	Pass           []string          `yaml:"pass,omitempty"`    // names or globs
	Set            map[string]string `yaml:"set,omitempty"`
}

// Configured reports whether c changes the inherited environment at all.
func (c CommandEnv) Configured() bool {
	return c.Inherit != "" || c.ExcludeSecrets || len(c.Exclude) > 0 || len(c.Pass) > 0 || len(c.Set) > 0
}

// ValidInherit reports whether mode is a known command_env.inherit value.
func ValidInherit(mode string) bool {
	return mode == "" || mode == "all" || mode == "core" || mode == "none"
}

// Validate checks the policy's names and globs. The error names the key.
func (c CommandEnv) Validate() error {
	if !ValidInherit(c.Inherit) {
		return fmt.Errorf("command_env.inherit %q must be all, core or none", c.Inherit)
	}
	for key, names := range map[string][]string{"exclude": c.Exclude, "pass": c.Pass} {
		for _, n := range names {
			if err := checkEnvName(n, true); err != nil {
				return fmt.Errorf("command_env.%s entry %q: %w", key, n, err)
			}
		}
	}
	for n := range c.Set {
		if err := checkEnvName(n, false); err != nil {
			return fmt.Errorf("command_env.set name %q: %w", n, err)
		}
	}
	return nil
}

func checkEnvName(n string, glob bool) error {
	switch {
	case n == "":
		return errors.New("empty name")
	case strings.Contains(n, "="):
		return errors.New("name must not contain =")
	case strings.ContainsRune(n, 0):
		return errors.New("name must not contain NUL")
	}
	if glob {
		if _, err := path.Match(n, ""); err != nil {
			return fmt.Errorf("malformed glob: %w", err)
		}
	} else if strings.ContainsAny(n, "*?[") {
		return errors.New("must be a plain name, not a glob")
	}
	return nil
}

// InvalidError is a config error that should exit with usage status 2.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// ValidEnvironment reports whether name is a known environment.
func ValidEnvironment(name string) bool {
	return name == "" || name == "local" || name == "container"
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
		MaxImageBytes:          DefaultMaxImageBytes,
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
		CommandEnv      yaml.Node `yaml:"command_env"`
	}
	if yaml.Unmarshal(data, &probe) == nil {
		if n := &probe.MaxFormatErrors; n.Kind != 0 && (n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int") {
			return cfg, fmt.Errorf("config %s: max_format_errors must be a whole number >= 1, got %q", path, n.Value)
		}
		if probe.CommandEnv.Kind != 0 {
			if err := checkCommandEnv(&probe.CommandEnv); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", path, err)}
			}
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
	if cfg.MaxImageBytes < 1 {
		return cfg, fmt.Errorf("config %s: max_image_bytes must be >= 1, got %d", path, cfg.MaxImageBytes)
	}
	if !ValidEnvironment(cfg.Environment) {
		return cfg, fmt.Errorf("config %s: environment %q must be local or container", path, cfg.Environment)
	}
	if err := cfg.CommandEnv.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", path, err)}
	}
	if r := cfg.Container.Runtime; r != "" && r != "docker" && r != "podman" {
		return cfg, fmt.Errorf("config %s: container.runtime %q must be docker or podman", path, r)
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

// checkCommandEnv rejects unknown keys and non-string set values under
// command_env, which the lenient YAML decoding would otherwise accept.
func checkCommandEnv(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return errors.New("command_env must be a mapping")
	}
	b, err := yaml.Marshal(n)
	if err != nil {
		return fmt.Errorf("command_env: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c CommandEnv
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("command_env: %w", err)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "set" {
			continue
		}
		set := n.Content[i+1]
		if set.Kind != yaml.MappingNode {
			if set.ShortTag() == "!!null" {
				continue
			}
			return errors.New("command_env.set must be a mapping of names to strings")
		}
		for j := 0; j+1 < len(set.Content); j += 2 {
			if v := set.Content[j+1]; v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" {
				return fmt.Errorf("command_env.set %s must be a string; quote it", set.Content[j].Value)
			}
		}
	}
	return nil
}
