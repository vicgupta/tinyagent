// Package config loads agent settings from an optional YAML file layered over defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/vicgupta/tinyagent/embed"
	"github.com/vicgupta/tinyagent/internal/sandboxnet"
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
	// SandboxAllowedDomains limits sandboxed commands' network to these
	// hosts (exact, "*.domain" or IP, optional ":port"), over HTTP and HTTPS
	// through a filtering proxy. Empty keeps the network denied.
	SandboxAllowedDomains []string `yaml:"sandbox_allowed_domains,omitempty"`
	// SandboxBackend picks the Linux sandbox engine: auto (default; bwrap
	// with Landlock stacked inside, else Landlock alone), landlock or bwrap.
	SandboxBackend string `yaml:"sandbox_backend,omitempty"`
	// TinyGo is an existing tinygo executable to build Go plugin modules
	// with, instead of the pinned release TinyAgent downloads on first use.
	// TinyGo is a separate project, never bundled with TinyAgent.
	TinyGo string `yaml:"tinygo,omitempty"`
	// Environment selects where bash and start_process commands run:
	// "local" (the default, also "") or "container".
	Environment string `yaml:"environment"`
	// Container configures the "container" environment.
	Container Container `yaml:"container"`
	// CommandEnv is the environment policy for local bash and start_process
	// commands. The zero value inherits tinyagent's whole environment.
	CommandEnv CommandEnv `yaml:"command_env"`
	// FileAccess denies the built-in file tools access to matching paths.
	// The zero value denies nothing.
	FileAccess FileAccess `yaml:"file_access"`
	// RedactSecrets masks known secret values in command, process, MCP,
	// web_fetch and hook output before the model sees it (off by default;
	// --redact-secrets overrides Enabled).
	RedactSecrets RedactSecrets `yaml:"redact_secrets"`
	// AdditionalDirs are directories outside --cwd that the built-in file
	// tools, --sandbox and the container environment may also reach. Empty
	// (the default) keeps everything inside --cwd. --add-dir-read and
	// --add-dir-write replace this list.
	AdditionalDirs []AdditionalDir `yaml:"additional_dirs,omitempty"`
	// Model is the provider/model used when neither -m nor $AGENT_MODEL sets
	// one. "" falls back to the built-in default.
	Model string `yaml:"model"`

	SystemPrompt string  `yaml:"system_prompt"`
	StepLimit    int     `yaml:"step_limit"`
	CostLimit    float64 `yaml:"cost_limit"`
	WallTimeSec  int     `yaml:"wall_time_seconds"`
	// TokenLimit caps the run's provider-reported input plus output
	// tokens, delegates and workers included; 0 (the default) = unlimited.
	TokenLimit int `yaml:"token_limit"`
	// MaxFormatErrors ends the run after this many consecutive steps whose
	// tool calls are all malformed. Must be >= 1; default 3.
	MaxFormatErrors int `yaml:"max_format_errors"`
	// MaxOutputTokens is the per-request output-token budget sent to the
	// provider; 0 (the default) keeps the provider default (8192 for
	// Anthropic). Not to be confused with MaxOutputChars, which caps tool
	// output.
	MaxOutputTokens int `yaml:"max_output_tokens"`
	// MaxTruncations ends the run after this many consecutive responses cut
	// off at the output-token limit. Must be >= 1; default 3.
	MaxTruncations int `yaml:"max_truncations"`

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

	// FallbackModels is the opt-in fallback chain: provider/model specs
	// tried in order when a request still fails with HTTP 429, 5xx or a
	// network error after the provider's retries. Empty (the default)
	// never switches. --fallback-model replaces it; --no-fallback clears it.
	FallbackModels []string `yaml:"fallback_models,omitempty"`

	// ReasoningEffort is sent to the provider's reasoning control: none,
	// minimal, low, medium, high, xhigh, or max. "" sends nothing, so the
	// model's default applies.
	ReasoningEffort string `yaml:"reasoning_effort"`

	// OpenAIAPI picks the openai provider's transport: auto (the default;
	// the Responses API for api.openai.com, Chat Completions when
	// OPENAI_BASE_URL points elsewhere), responses, or chat. Other providers
	// ignore it.
	OpenAIAPI string `yaml:"openai_api"`

	// ClaudeCode and CodexExec configure the claude-code and codex-exec
	// handoff providers (docs/models.md). Unset looks the CLI up on PATH.
	ClaudeCode HandoffCLI `yaml:"claude_code,omitempty"`
	CodexExec  HandoffCLI `yaml:"codex_exec,omitempty"`

	MCPServers []MCPServer `yaml:"mcp_servers,omitempty"`

	// Plugins lists Agent Plugins package directories (docs/plugins.md).
	Plugins []Plugin `yaml:"plugins,omitempty"`

	// Providers are named model providers selectable as "<id>/<model>".
	// They come only from -c or a trusted ./tinyagent.yaml (--trust-config).
	Providers []Provider `yaml:"providers,omitempty"`

	// Hooks are trusted local executables run around matching tool calls.
	Hooks Hooks `yaml:"hooks,omitempty"`

	// Permissions is the opt-in tool permission policy: allow/ask/deny rules
	// matched on the tool name and its main argument, applied before hooks.
	// The zero value enforces nothing.
	Permissions Permissions `yaml:"permissions"`

	// CommandTools are user-defined tools that run a fixed argv filled in
	// with schema-checked arguments, without a shell. They come only from
	// -c or a trusted ./tinyagent.yaml (--trust-config).
	CommandTools []CommandTool `yaml:"command_tools,omitempty"`

	// Plan configures the opt-in update_plan tool (off by default).
	Plan Plan `yaml:"plan,omitempty"`
	// VerifyOnStop configures the opt-in verify-before-stop guard (off by
	// default).
	VerifyOnStop VerifyOnStop `yaml:"verify_on_stop"`
	// Images configures the opt-in view_image tool and MCP image
	// pass-through (off by default).
	Images Images `yaml:"images,omitempty"`
	// MaxImageBytes caps one image's raw size. Must be >= 1; default 5 MiB.
	MaxImageBytes int `yaml:"max_image_bytes"`
	// ApplyPatch offers the opt-in apply_patch tool (off by default;
	// --apply-patch overrides it).
	ApplyPatch bool `yaml:"apply_patch"`
	// ScopedAgentsMD also delivers AGENTS.md files in directories below
	// --cwd with the results of file tools touching paths there (off by
	// default; --scoped-agents-md overrides it).
	ScopedAgentsMD bool `yaml:"scoped_agents_md"`
	// Subagents configures the opt-in delegate tool, which runs a
	// read-only child agent (off by default; --subagents overrides
	// Enabled).
	Subagents Subagents `yaml:"subagents"`
	// Models routes roles and tasks to their own models (empty by
	// default: every task uses the main model).
	Models Models `yaml:"models,omitempty"`
	// WebFetch configures the opt-in web_fetch tool (off by default;
	// --web-fetch overrides Enabled).
	WebFetch WebFetch `yaml:"web_fetch"`
	// AskUser configures the opt-in ask_user tool, which ends a run with
	// one question for the user (off by default; --ask-user overrides
	// Enabled).
	AskUser AskUser `yaml:"ask_user"`
	// LSP configures opt-in language servers: diagnostics appended to edit
	// results and the read-only lsp tool (off by default; --lsp overrides
	// Enabled). It comes only from -c or a trusted ./tinyagent.yaml (--trust-config).
	LSP LSP `yaml:"lsp"`
	// Skills turns on Agent Skills discovery from .agents/skills (off by
	// default; --skills / --no-skills override it).
	Skills bool `yaml:"skills"`
	// Memory configures opt-in cross-run project memory (off by default;
	// --memory overrides Enabled). It comes only from -c or a trusted ./tinyagent.yaml (--trust-config).
	Memory Memory `yaml:"memory"`
	// Handoff configures the opt-in session handoff file (off by default;
	// --handoff overrides Enabled). It comes only from -c or a trusted ./tinyagent.yaml (--trust-config).
	Handoff Handoff `yaml:"handoff"`
	// LogDB is the opt-in SQLite event log.
	LogDB LogDB `yaml:"log_db"`
	// OTel is the opt-in OpenTelemetry (OTLP/HTTP) trace export.
	OTel OTel `yaml:"otel"`
	// ToolOutputFiles saves the full text of over-limit tool results to
	// per-run files the model can page (off by default;
	// --tool-output-files overrides Enabled).
	ToolOutputFiles ToolOutputFiles `yaml:"tool_output_files"`
	// Snapshots configures opt-in workspace snapshots for --rewind (off by
	// default; --snapshots overrides enabled). It may be written as a bare
	// bool (snapshots: true) or as a mapping.
	Snapshots Snapshots `yaml:"snapshots"`
	// ACP configures the --acp editor mode.
	ACP ACP `yaml:"acp"`
	// UntrustedContent configures the opt-in untrusted-content lockdown
	// (off by default; --lockdown overrides Enabled).
	UntrustedContent UntrustedContent `yaml:"untrusted_content"`
	// Attempts configures --attempts best-of-N runs.
	Attempts Attempts `yaml:"attempts"`
}

// ACP configures the --acp editor mode.
type ACP struct {
	// ClientMCPServers starts the MCP servers an ACP client sends with
	// session/new, exposing all their tools (off by default: they are
	// ignored and named in the session's first message).
	ClientMCPServers bool `yaml:"client_mcp_servers"`
}

// Snapshots configures workspace snapshots.
type Snapshots struct {
	Enabled bool `yaml:"enabled"`
	// MaxFileBytes is the largest file captured; larger files are skipped
	// and reported. Must be >= 1; default 2 MiB.
	MaxFileBytes int64 `yaml:"max_file_bytes"`
}

// DefaultSnapshotMaxFileBytes is the default snapshots.max_file_bytes.
const DefaultSnapshotMaxFileBytes = 2 << 20

// UnmarshalYAML accepts "snapshots: true" as well as the mapping form, and
// rejects unknown keys in the mapping.
func (s *Snapshots) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!bool" {
		return n.Decode(&s.Enabled)
	}
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return errors.New("snapshots must be true, false, or a mapping with enabled and max_file_bytes")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch k, v := n.Content[i].Value, n.Content[i+1]; k {
		case "enabled":
			if v.ShortTag() != "!!bool" {
				return fmt.Errorf("snapshots.enabled must be true or false, got %q", v.Value)
			}
			if err := v.Decode(&s.Enabled); err != nil {
				return err
			}
		case "max_file_bytes":
			if v.ShortTag() != "!!int" {
				return fmt.Errorf("snapshots.max_file_bytes must be a whole number >= 1, got %q", v.Value)
			}
			if err := v.Decode(&s.MaxFileBytes); err != nil {
				return err
			}
		default:
			return fmt.Errorf("snapshots: unknown key %q", k)
		}
	}
	return nil
}

// ToolOutputFiles configures saving over-limit tool output.
type ToolOutputFiles struct {
	Enabled bool `yaml:"enabled"`
	// Dir is relative to --cwd and must stay inside it; "" means
	// DefaultToolOutputDir.
	Dir string `yaml:"dir"`
	// MaxRuns keeps this many earlier run directories at run start; 0
	// keeps all.
	MaxRuns int `yaml:"max_runs"`
}

// DefaultToolOutputDir is the default tool_output_files.dir.
const DefaultToolOutputDir = ".tinyagent/tool-output"

// DefaultToolOutputMaxRuns is the default tool_output_files.max_runs.
const DefaultToolOutputMaxRuns = 20

// Images turns on sending images to vision-capable models.
type Images struct {
	Enabled bool `yaml:"enabled"`
}

// DefaultMaxImageBytes is the default max_image_bytes (5 MiB).
const DefaultMaxImageBytes = 5 << 20

// WebFetch configures the opt-in web_fetch tool, which reads allowlisted
// URLs in tinyagent's own process, separately from command network access.
type WebFetch struct {
	Enabled bool `yaml:"enabled"`
	// AllowedDomains lists the hosts web_fetch may read, in the
	// sandbox_allowed_domains syntax. Required when Enabled.
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
	// MaxBytes caps the body read per fetch. Must be >= 1; default 2 MiB.
	MaxBytes int `yaml:"max_bytes"`
	// TimeoutSeconds caps each fetch, redirects included (1..120;
	// default 20).
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// Web fetch defaults and limits.
const (
	DefaultWebFetchMaxBytes       = 2 << 20
	DefaultWebFetchTimeoutSeconds = 20
	MaxWebFetchTimeoutSeconds     = 120
)

// Validate checks the limits and allowlist syntax. An empty allowlist with
// Enabled is checked after flags apply (see the CLI).
func (w WebFetch) Validate() error {
	if w.MaxBytes < 1 {
		return fmt.Errorf("web_fetch.max_bytes must be >= 1, got %d", w.MaxBytes)
	}
	if w.TimeoutSeconds < 1 || w.TimeoutSeconds > MaxWebFetchTimeoutSeconds {
		return fmt.Errorf("web_fetch.timeout_seconds must be between 1 and %d, got %d", MaxWebFetchTimeoutSeconds, w.TimeoutSeconds)
	}
	if _, err := sandboxnet.ParseRules(w.AllowedDomains); err != nil {
		return fmt.Errorf("web_fetch.allowed_domains %v", err)
	}
	return nil
}

// Subagents configures the opt-in delegate tool.
type Subagents struct {
	Enabled bool `yaml:"enabled"`
	// Model is the provider/model children use; "" means the parent's
	// model.
	Model string `yaml:"model"`
	// MaxSteps caps each child's model calls; 0 means
	// DefaultSubagentMaxSteps.
	MaxSteps int `yaml:"max_steps"`
	// Workers configures the opt-in worker tool (--workers), whose
	// children may edit files and run commands in their own Git worktree.
	// nil when the config has no subagents.workers section.
	Workers *Workers `yaml:"workers"`
}

// Workers configures the opt-in worker and apply_worker tools.
type Workers struct {
	Enabled bool `yaml:"enabled"`
	// Model is the provider/model workers use; "" means the parent's.
	Model string `yaml:"model"`
	// MaxSteps caps each worker's model calls; 0 means
	// DefaultWorkerMaxSteps.
	MaxSteps int `yaml:"max_steps"`
	// MaxConcurrent is the cap on workers running at once (1..4; 0 means
	// 1). Worker calls in one response run concurrently up to this cap.
	MaxConcurrent int `yaml:"max_concurrent"`
	// Tools are the tools a worker may use, intersected with the parent's;
	// empty means DefaultWorkerTools.
	Tools []string `yaml:"tools"`
}

// DefaultWorkerMaxSteps is a worker's step cap when
// subagents.workers.max_steps is 0.
const DefaultWorkerMaxSteps = 60

// DefaultWorkerTools are a worker's tools when subagents.workers.tools is
// empty.
var DefaultWorkerTools = []string{"read_file", "list_files", "grep", "write_file", "edit_file", "apply_patch", "bash"}

// WorkerToolChoices are the tool names subagents.workers.tools accepts.
var WorkerToolChoices = []string{"read_file", "list_files", "grep", "write_file", "edit_file", "apply_patch", "bash", "start_process", "read_process", "stop_process"}

// validateWorkers checks subagents.workers.
func validateWorkers(w *Workers) error {
	if w == nil {
		return nil
	}
	if w.MaxSteps < 0 {
		return fmt.Errorf("subagents.workers.max_steps must be a whole number >= 0, got %d", w.MaxSteps)
	}
	if w.MaxConcurrent < 0 || w.MaxConcurrent > 4 {
		return fmt.Errorf("subagents.workers.max_concurrent must be a whole number from 1 to 4, got %d", w.MaxConcurrent)
	}
	for _, t := range w.Tools {
		ok := false
		for _, c := range WorkerToolChoices {
			if t == c {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("subagents.workers.tools: unknown tool %q (choose from %s)", t, strings.Join(WorkerToolChoices, ", "))
		}
	}
	return nil
}

// DefaultSubagentMaxSteps is the step cap of a delegate child when
// subagents.max_steps is 0.
const DefaultSubagentMaxSteps = 30

// AskUser configures the ask_user tool.
type AskUser struct {
	Enabled bool `yaml:"enabled"`
	// MaxQuestions caps the questions one run may ask, counted across
	// its whole resume chain: 1 to MaxAskUserQuestions.
	MaxQuestions int `yaml:"max_questions"`
}

// DefaultAskUserMaxQuestions is the default ask_user.max_questions, and
// MaxAskUserQuestions its upper bound.
const (
	DefaultAskUserMaxQuestions = 3
	MaxAskUserQuestions        = 20
)

// Plan configures the persistent plan tool.
type Plan struct {
	Enabled bool `yaml:"enabled"`
	// RequireFirst makes the agent record a plan with update_plan before
	// its first workspace-changing tool call (off by default so one-shot
	// questions and read-only lookups aren't slowed down; --plan-first
	// turns it and the plan tool on).
	RequireFirst bool `yaml:"require_first"`
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

// OTel configures the opt-in OpenTelemetry trace export. It is off unless
// Enabled (or --otel) is set; OTEL_* variables alone never turn it on. Empty
// fields fall back to the standard OTEL_EXPORTER_OTLP_* variables.
type OTel struct {
	Enabled bool `yaml:"enabled"`
	// Endpoint is the OTLP/HTTP base URL; /v1/traces is appended.
	Endpoint string `yaml:"endpoint"`
	// Protocol is http/protobuf or http/json.
	Protocol string `yaml:"protocol"`
	// HeadersEnv names environment variables whose values are k=v,k2=v2
	// header lists. Header values never come from the config itself.
	HeadersEnv []string `yaml:"headers_env"`
	// Timeout bounds each export, retries included, and the final flush,
	// as a Go duration such as "10s".
	Timeout string `yaml:"timeout"`
	// ServiceName is the resource service.name.
	ServiceName string `yaml:"service_name"`
	// Content adds redacted task, assistant, argument and output text.
	Content bool `yaml:"content"`
	// MaxAttributeBytes caps each content attribute; 0 means 4096.
	MaxAttributeBytes int `yaml:"max_attribute_bytes"`
}

// OTelProtocols are the supported OTLP transports.
var OTelProtocols = []string{"http/protobuf", "http/json"}

// ValidOTelProtocol reports whether p is a supported OTLP protocol.
func ValidOTelProtocol(p string) bool { return slices.Contains(OTelProtocols, p) }

// OTelProtocolError is the error for an unsupported OTLP protocol from src.
func OTelProtocolError(src, p string) error {
	return fmt.Errorf("%s %q is not supported; use %s (OTLP/gRPC is not supported)", src, p, strings.Join(OTelProtocols, " or "))
}

// Validate checks the settings that don't depend on the environment.
func (o OTel) Validate() error {
	if o.Endpoint != "" {
		if u, err := url.Parse(o.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("otel.endpoint %q must be an http:// or https:// URL", o.Endpoint)
		}
	}
	if o.Protocol != "" && !ValidOTelProtocol(o.Protocol) {
		return OTelProtocolError("otel.protocol", o.Protocol)
	}
	if o.Timeout != "" {
		if d, err := time.ParseDuration(o.Timeout); err != nil || d <= 0 {
			return fmt.Errorf("otel.timeout %q must be a positive duration such as 10s", o.Timeout)
		}
	}
	for _, n := range o.HeadersEnv {
		if n == "" || strings.ContainsAny(n, "= ") {
			return fmt.Errorf("otel.headers_env entry %q must be an environment variable name", n)
		}
	}
	if o.MaxAttributeBytes < 0 {
		return fmt.Errorf("otel.max_attribute_bytes must be >= 0, got %d", o.MaxAttributeBytes)
	}
	return nil
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

// Permissions is the opt-in tool permission policy. A rule matches a tool
// call by tool name and, optionally, one primary argument (the command text,
// a path, or a URL host). Decisions are applied deny > ask > allow > default.
type Permissions struct {
	// Default is the decision when no rule matches: allow (default), ask or
	// deny.
	Default string `yaml:"default"`
	// NonInteractive is what an ask becomes when nobody can answer (no
	// terminal, --json, or an ACP client that does not answer): deny
	// (default) or allow.
	NonInteractive string `yaml:"non_interactive"`
	// Rules are evaluated in order; a deny or ask matches when any of its
	// patterns match, and an allow matches only when every compound-command
	// segment is covered. A profile's rules replace the base list.
	Rules []PermissionRule `yaml:"rules,omitempty"`
}

// PermissionRule is one permissions.rules entry.
type PermissionRule struct {
	// Tool is a "|"-separated list of tool-name globs ("*" wildcard). It is
	// required.
	Tool string `yaml:"tool"`
	// Command matches the shell command text (bash, start_process, and
	// command tools' resolved argv). At most one of command, path and host
	// may be set.
	Command string `yaml:"command,omitempty"`
	// Path matches a path relative to --cwd in the file_access syntax (for
	// apply_patch, when any path matches).
	Path string `yaml:"path,omitempty"`
	// Host matches a web_fetch URL host in the sandbox_allowed_domains
	// syntax.
	Host string `yaml:"host,omitempty"`
	// Decision is allow, ask or deny; it is required.
	Decision string `yaml:"decision"`
	// Message is shown to the model on deny.
	Message string `yaml:"message,omitempty"`
}

// Configured reports whether any permission rule or setting is present.
func (p Permissions) Configured() bool {
	return p.Default != "" || p.NonInteractive != "" || len(p.Rules) > 0
}

// Permissions decisions.
const (
	DecisionAllow = "allow"
	DecisionAsk   = "ask"
	DecisionDeny  = "deny"
)

// Validate checks the policy's settings and rules. The error names the
// offending key and rule index.
func (p Permissions) Validate() error {
	switch p.Default {
	case "", DecisionAllow, DecisionAsk, DecisionDeny:
	default:
		return fmt.Errorf("permissions.default %q must be allow, ask or deny", p.Default)
	}
	switch p.NonInteractive {
	case "", DecisionDeny, DecisionAllow:
	default:
		return fmt.Errorf("permissions.non_interactive %q must be deny or allow", p.NonInteractive)
	}
	for i, r := range p.Rules {
		if err := r.validate(i); err != nil {
			return err
		}
	}
	return nil
}

func (r PermissionRule) validate(i int) error {
	where := fmt.Sprintf("permissions.rules[%d]", i)
	if strings.TrimSpace(r.Tool) == "" {
		return fmt.Errorf("%s: tool is required", where)
	}
	switch r.Decision {
	case DecisionAllow, DecisionAsk, DecisionDeny:
	default:
		return fmt.Errorf("%s: decision must be allow, ask or deny, got %q", where, r.Decision)
	}
	n := 0
	for _, v := range []struct{ key, val string }{
		{"command", r.Command}, {"path", r.Path}, {"host", r.Host},
	} {
		if v.val != "" {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("%s: at most one of command, path and host may be set", where)
	}
	// Validate the tool-name globs and, where a literal built-in name makes
	// the matcher family unambiguous, reject a mismatched matcher.
	for _, t := range strings.Split(r.Tool, "|") {
		t = strings.TrimSpace(t)
		if t == "" {
			return fmt.Errorf("%s: tool %q has an empty name", where, r.Tool)
		}
		if strings.ContainsAny(t, "*?[]") {
			if strings.ContainsAny(t, "?[]") {
				return fmt.Errorf("%s: tool %q: only the * wildcard is supported", where, t)
			}
			continue // a glob can't be checked against a matcher family
		}
		if r.Command != "" && (fileFamilyTools[t] || t == "web_fetch") {
			return fmt.Errorf("%s: command matches shell commands; it does not apply to %q", where, t)
		}
		if r.Path != "" && (commandFamilyTools[t] || t == "web_fetch") {
			return fmt.Errorf("%s: path matches file tools; it does not apply to %q", where, t)
		}
		if r.Host != "" && t != "web_fetch" {
			return fmt.Errorf("%s: host matches web_fetch URLs; it does not apply to %q", where, t)
		}
	}
	if r.Path != "" {
		if err := validateAccessPattern(r.Path); err != nil {
			return fmt.Errorf("%s: path: %v", where, err)
		}
	}
	if r.Host != "" {
		if _, err := sandboxnet.ParseRule(r.Host); err != nil {
			return fmt.Errorf("%s: host: %v", where, err)
		}
	}
	return nil
}

// fileFamilyTools are the built-in tools a path matcher applies to.
var fileFamilyTools = map[string]bool{
	"read_file": true, "write_file": true, "edit_file": true, "apply_patch": true, "undo_file": true,
	"list_files": true, "grep": true, "view_image": true,
}

// commandFamilyTools are the built-in tools a command matcher applies to.
var commandFamilyTools = map[string]bool{"bash": true, "start_process": true}

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
// ValidSandboxBackend reports whether b is a sandbox_backend value: auto
// (or empty), landlock or bwrap.
func ValidSandboxBackend(b string) bool {
	return b == "" || b == "auto" || b == "landlock" || b == "bwrap"
}

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

// RedactSecrets configures masking of known secret values in tool output.
type RedactSecrets struct {
	Enabled bool `yaml:"enabled"`
	// Env names more variables (names or * / ? globs) whose values are
	// secrets, beyond the secret-named ones.
	Env []string `yaml:"env,omitempty"`
	// Allow names variables (names or globs) not to treat as secrets.
	Allow []string `yaml:"allow,omitempty"`
	// Patterns also masks token-shaped strings (sk-..., ghp_..., Bearer ...).
	Patterns bool `yaml:"patterns"`
}

// Validate checks env and allow. The error names the key.
func (r RedactSecrets) Validate() error {
	for key, names := range map[string][]string{"env": r.Env, "allow": r.Allow} {
		for _, n := range names {
			if err := checkEnvName(n, true); err != nil {
				return fmt.Errorf("redact_secrets.%s entry %q: %w", key, n, err)
			}
		}
	}
	for _, n := range r.Env {
		if slices.Contains(r.Allow, n) {
			return fmt.Errorf("redact_secrets: %q is in both env and allow", n)
		}
	}
	return nil
}

// AdditionalDir is one additional_dirs entry.
type AdditionalDir struct {
	// Path is an absolute directory outside --cwd.
	Path string `yaml:"path"`
	// Mode is "read" or "write"; it is required.
	Mode string `yaml:"mode"`
}

// validateAdditionalDirs checks the config-level rules for additional_dirs:
// absolute paths and an explicit read or write mode. Existence and overlap
// with --cwd are checked once --cwd is known.
func validateAdditionalDirs(dirs []AdditionalDir) error {
	for i, d := range dirs {
		if d.Path == "" {
			return fmt.Errorf("additional_dirs entry %d: path is required", i+1)
		}
		if !filepath.IsAbs(d.Path) {
			return fmt.Errorf("additional_dirs entry %q must be an absolute path", d.Path)
		}
		switch d.Mode {
		case "read", "write":
		case "":
			return fmt.Errorf("additional_dirs entry %q: mode is required (read or write)", d.Path)
		default:
			return fmt.Errorf("additional_dirs entry %q: mode %q must be read or write", d.Path, d.Mode)
		}
	}
	return nil
}

// FileAccess lists gitignore-style patterns, relative to --cwd, that the
// built-in file tools refuse. DenyRead paths are also write-denied. It does
// not apply to bash, start_process, hooks or MCP servers.
type FileAccess struct {
	DenyRead  []string `yaml:"deny_read,omitempty"`
	DenyWrite []string `yaml:"deny_write,omitempty"`
}

// Configured reports whether any rule is set.
func (f FileAccess) Configured() bool { return len(f.DenyRead)+len(f.DenyWrite) > 0 }

// Validate rejects empty patterns, ! negation (reserved for later) and bad
// glob syntax.
func (f FileAccess) Validate() error {
	for _, l := range []struct {
		key string
		ps  []string
	}{{"deny_read", f.DenyRead}, {"deny_write", f.DenyWrite}} {
		for _, p := range l.ps {
			if err := validateAccessPattern(p); err != nil {
				return fmt.Errorf("file_access.%s: %w", l.key, err)
			}
		}
	}
	return nil
}

func validateAccessPattern(p string) error {
	s := strings.TrimSpace(p)
	if s == "" {
		return errors.New("empty pattern")
	}
	if strings.HasPrefix(s, "!") {
		return fmt.Errorf("pattern %q: ! negation is not supported", p)
	}
	s = strings.Trim(filepath.ToSlash(s), "/")
	if s == "" {
		return fmt.Errorf("pattern %q matches nothing", p)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("pattern %q: empty, . or .. path segment", p)
		}
		if seg != "**" {
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Errorf("pattern %q: %w", p, err)
			}
		}
	}
	return nil
}

// validateMCPServers checks each server's allowlists: a nonempty tools or
// resources list, and well-formed resource patterns.
func validateMCPServers(servers []MCPServer) error {
	for _, def := range servers {
		if len(def.Tools) == 0 && len(def.Resources) == 0 {
			return fmt.Errorf("mcp server %q: a nonempty tools or resources allowlist is required", def.Alias)
		}
		if err := ValidateResourcePatterns(def.Resources); err != nil {
			return fmt.Errorf("mcp server %q: %w", def.Alias, err)
		}
	}
	return nil
}

// ValidateResourcePatterns rejects empty, duplicate and bare "*" resource
// patterns, and "*" anywhere but at the end.
func ValidateResourcePatterns(patterns []string) error {
	seen := map[string]bool{}
	for _, p := range patterns {
		switch {
		case p == "" || p == "*":
			return fmt.Errorf("resources pattern %q must be a URI or a URI prefix ending in *", p)
		case strings.Contains(strings.TrimSuffix(p, "*"), "*"):
			return fmt.Errorf("resources pattern %q: * is allowed only at the end", p)
		case seen[p]:
			return fmt.Errorf("resources pattern %q is duplicated", p)
		}
		seen[p] = true
	}
	return nil
}

// MatchResource reports whether uri matches one of patterns: equal to an
// exact pattern, or starting with the text before a trailing "*". URIs are
// compared literally, without normalization or decoding.
func MatchResource(patterns []string, uri string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(uri, prefix) {
				return true
			}
		} else if uri == p {
			return true
		}
	}
	return false
}

// InvalidError is a config error that should exit with usage status 2.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// ValidEnvironment reports whether name is a known environment.
func ValidEnvironment(name string) bool {
	return name == "" || name == "local" || name == "container"
}

// CommandTool is one command_tools entry. tools.NewCommandTools checks it.
type CommandTool struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Parameters is the JSON Schema of the arguments, in the
	// --output-schema subset; nil means no arguments.
	Parameters map[string]any `yaml:"parameters,omitempty"`
	// Argv is the command; an element that is exactly {{name}} is replaced
	// by the argument name.
	Argv []string `yaml:"argv"`
	// TimeoutSec is the per-call timeout; 0 means bash_timeout_seconds.
	TimeoutSec int `yaml:"timeout_seconds,omitempty"`
	// Dir is the working directory, relative to --cwd and inside it; ""
	// means --cwd.
	Dir string `yaml:"dir,omitempty"`
}

// MCPServer grants access to selected tools and resources from one local
// stdio server. At least one of Tools and Resources must be nonempty.
type MCPServer struct {
	Alias   string   `yaml:"alias"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args,omitempty"`
	Tools   []string `yaml:"tools"`
	// Resources allowlists the resource URIs the model may list and read:
	// each pattern is an exact URI or a literal prefix ending in "*".
	Resources           []string `yaml:"resources,omitempty"`
	Env                 []string `yaml:"env,omitempty"`
	DiscoveryTimeoutSec int      `yaml:"discovery_timeout_seconds"`
	CallTimeoutSec      int      `yaml:"call_timeout_seconds"`
	// Untrusted false marks the server's results as trusted: with
	// untrusted-content lockdown on they don't taint the run. Absent or
	// true (the default) means they taint.
	Untrusted *bool `yaml:"untrusted,omitempty"`
	// LockdownAllowed keeps the server's tools and resources callable after
	// lockdown taints the run (off by default: they are refused).
	LockdownAllowed bool `yaml:"lockdown_allowed,omitempty"`
	// SetEnv holds NAME=value pairs set for the server, and AllTools
	// exposes every tool it lists. Only servers an ACP client supplies
	// use them; a config file can't set either.
	SetEnv   []string `yaml:"-"`
	AllTools bool     `yaml:"-"`
	// Dir, when set, is the server's working directory instead of --cwd.
	// Only plugin servers (from a plugin's mcp.json) use it.
	Dir string `yaml:"-"`
}

// Provider is one config-defined model provider. Secrets come only from the
// environment variables it names, never from the file itself.
type Provider struct {
	ID      string `yaml:"id"`
	API     string `yaml:"api"` // chat, responses or anthropic
	BaseURL string `yaml:"base_url"`
	// APIKeyEnv names the variable holding the API key; "" sends no key.
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
	// Headers are static, non-secret request headers.
	Headers map[string]string `yaml:"headers,omitempty"`
	// EnvHeaders maps a header name to the variable holding its value.
	EnvHeaders map[string]string `yaml:"env_headers,omitempty"`
	// Models holds per-model metadata, matched by exact model name.
	Models map[string]ProviderModel `yaml:"models,omitempty"`
}

// ProviderModel is price and context-window metadata for one model of a
// configured provider. Zero values mean unknown.
type ProviderModel struct {
	InputPricePerMTok  float64 `yaml:"input_price_per_mtok"`
	OutputPricePerMTok float64 `yaml:"output_price_per_mtok"`
	// CacheReadMultiplier is the cache-read price as a multiple of the
	// input price, in (0, 1]; 0 means the default 0.1.
	CacheReadMultiplier float64 `yaml:"cache_read_multiplier"`
	ContextWindow       int     `yaml:"context_window"`
}

// ProviderAPIs are the accepted providers[].api values.
var ProviderAPIs = []string{"chat", "responses", "anthropic"}

// HandoffCLI configures a provider that runs an installed coding-agent CLI.
type HandoffCLI struct {
	// Path is the CLI binary to run; "" looks it up on PATH. A leading ~/
	// is expanded.
	Path string `yaml:"path,omitempty"`
}

// BuiltinProviders are reserved and can't be used as a providers[].id.
var BuiltinProviders = []string{"openai", "openrouter", "anthropic", "ollama", "codex", "claude-code", "codex-exec"}

// ErrInvalidProvider wraps every providers validation error, so callers can
// exit 2 for them.
var ErrInvalidProvider = errors.New("invalid provider")

// DefaultMaxFormatErrors is the default MaxFormatErrors.
const DefaultMaxFormatErrors = 3

// DefaultMaxTruncations is the default MaxTruncations.
const DefaultMaxTruncations = 3

func Default() AgentConfig {
	return AgentConfig{
		SystemPrompt:   embed.SystemPrompt(),
		BashTimeoutSec: 120,
		MaxOutputChars: 30000,

		MaxBackgroundProcesses: 4,
		MaxFormatErrors:        DefaultMaxFormatErrors,
		MaxTruncations:         DefaultMaxTruncations,
		MaxImageBytes:          DefaultMaxImageBytes,
		WebFetch:               WebFetch{MaxBytes: DefaultWebFetchMaxBytes, TimeoutSeconds: DefaultWebFetchTimeoutSeconds},
		LSP:                    DefaultLSP(),
		AskUser:                AskUser{MaxQuestions: DefaultAskUserMaxQuestions},
		LogDB:                  LogDB{Level: "metadata", MaxDays: 30, MaxRuns: 500},
		ToolOutputFiles:        ToolOutputFiles{Dir: DefaultToolOutputDir, MaxRuns: DefaultToolOutputMaxRuns},
		Snapshots:              Snapshots{MaxFileBytes: DefaultSnapshotMaxFileBytes},
		Memory:                 DefaultMemory(),
		Handoff:                DefaultHandoff(),
		VerifyOnStop:           VerifyOnStop{MaxReprompts: DefaultVerifyMaxReprompts},
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
	return LoadProfile(path, "")
}

// LoadProfile is Load with the named profile from the file's profiles:
// section merged over the base settings before decoding and validation. An
// empty profile applies none; the profiles: section is still validated.
// With a profile selected, every invalid-config error is an *InvalidError
// and names the profile.
func LoadProfile(path, profile string) (AgentConfig, error) {
	if profile == "" {
		return loadFile(path, "")
	}
	if path == "" {
		return Default(), &InvalidError{fmt.Errorf("--profile %s: no config file defines profiles (no -c given and no ./%s in --cwd)", profile, ProjectFile)}
	}
	cfg, err := loadFile(path, profile)
	var invalid *InvalidError
	if err != nil && !errors.As(err, &invalid) && !errors.Is(err, fs.ErrNotExist) && !strings.HasPrefix(err.Error(), "read config") {
		err = &InvalidError{err}
	}
	return cfg, err
}

func loadFile(path, profile string) (AgentConfig, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if data, err = applyProfiles(path, data, profile); err != nil {
		return cfg, err
	}
	src := path
	if profile != "" {
		src = fmt.Sprintf("%s (profile %s)", path, profile)
	}
	var probe struct {
		MaxFormatErrors yaml.Node `yaml:"max_format_errors"`
		MaxOutputTokens yaml.Node `yaml:"max_output_tokens"`
		MaxTruncations  yaml.Node `yaml:"max_truncations"`
		CommandEnv      yaml.Node `yaml:"command_env"`
		LSP             yaml.Node `yaml:"lsp"`
		RedactSecrets   yaml.Node `yaml:"redact_secrets"`
		OTel            yaml.Node `yaml:"otel"`
		Permissions     yaml.Node `yaml:"permissions"`
		Memory          yaml.Node `yaml:"memory"`
		Handoff         yaml.Node `yaml:"handoff"`
		// UntrustedContent is checked strictly here so a typo under
		// untrusted_content exits 2 like any other lockdown config error.
		UntrustedContent yaml.Node `yaml:"untrusted_content"`
		Attempts         yaml.Node `yaml:"attempts"`
		Plugins          yaml.Node `yaml:"plugins"`
	}
	if yaml.Unmarshal(data, &probe) == nil {
		for _, c := range []struct {
			key, want string
			n         *yaml.Node
		}{
			{"max_format_errors", ">= 1", &probe.MaxFormatErrors},
			{"max_output_tokens", ">= 0", &probe.MaxOutputTokens},
			{"max_truncations", ">= 1", &probe.MaxTruncations},
		} {
			if n := c.n; n.Kind != 0 && (n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int") {
				return cfg, fmt.Errorf("config %s: %s must be a whole number %s, got %q", src, c.key, c.want, n.Value)
			}
		}
		if probe.CommandEnv.Kind != 0 {
			if err := checkCommandEnv(&probe.CommandEnv); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.LSP.Kind != 0 {
			if err := checkStrict(&probe.LSP, "lsp", &LSP{}); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.OTel.Kind != 0 {
			if err := checkStrict(&probe.OTel, "otel", &OTel{}); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.RedactSecrets.Kind != 0 {
			if err := checkRedactSecrets(&probe.RedactSecrets); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.Memory.Kind != 0 {
			if err := checkStrict(&probe.Memory, "memory", &Memory{}); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.Handoff.Kind != 0 {
			if err := checkStrict(&probe.Handoff, "handoff", &Handoff{}); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.Permissions.Kind != 0 {
			if err := checkStrict(&probe.Permissions, "permissions", &Permissions{}); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.UntrustedContent.Kind != 0 {
			if err := checkUntrustedContent(&probe.UntrustedContent); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.Plugins.Kind != 0 {
			if err := checkPlugins(&probe.Plugins); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
		if probe.Attempts.Kind != 0 {
			if err := checkAttempts(&probe.Attempts); err != nil {
				return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
			}
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) { // io.EOF: empty or comment-only file
		return cfg, fmt.Errorf("parse config %s: %w", src, err)
	}
	if l := cfg.LogDB.Level; l != "metadata" && l != "full" {
		return cfg, fmt.Errorf("config %s: log_db.level %q must be metadata or full", src, l)
	}
	if cfg.LogDB.MaxDays < 0 || cfg.LogDB.MaxRuns < 0 || cfg.LogDB.MaxPayloadBytes < 0 {
		return cfg, fmt.Errorf("config %s: log_db max_days, max_runs and max_payload_bytes must be >= 0", src)
	}
	if cfg.ToolOutputFiles.MaxRuns < 0 {
		return cfg, fmt.Errorf("config %s: tool_output_files.max_runs must be >= 0, got %d", src, cfg.ToolOutputFiles.MaxRuns)
	}
	if d := cfg.ToolOutputFiles.Dir; d != "" && !filepath.IsAbs(d) {
		if c := filepath.Clean(d); c == "." || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
			return cfg, fmt.Errorf("config %s: tool_output_files.dir %q must be a subdirectory of --cwd", src, d)
		}
	}
	if cfg.MaxFormatErrors < 1 {
		return cfg, fmt.Errorf("config %s: max_format_errors must be a whole number >= 1, got %d", src, cfg.MaxFormatErrors)
	}
	if cfg.MaxOutputTokens < 0 {
		return cfg, fmt.Errorf("config %s: max_output_tokens must be a whole number >= 0, got %d", src, cfg.MaxOutputTokens)
	}
	if err := validateWorkers(cfg.Subagents.Workers); err != nil {
		return cfg, fmt.Errorf("config %s: %w", src, err)
	}
	if err := validateModels(&cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", src, err)
	}
	if cfg.Subagents.MaxSteps < 0 {
		return cfg, fmt.Errorf("config %s: subagents.max_steps must be a whole number >= 0, got %d", src, cfg.Subagents.MaxSteps)
	}
	if n := cfg.AskUser.MaxQuestions; n < 1 || n > MaxAskUserQuestions {
		return cfg, fmt.Errorf("config %s: ask_user.max_questions must be a whole number from 1 to %d, got %d", src, MaxAskUserQuestions, n)
	}
	if cfg.MaxTruncations < 1 {
		return cfg, fmt.Errorf("config %s: max_truncations must be a whole number >= 1, got %d", src, cfg.MaxTruncations)
	}
	if cfg.Snapshots.MaxFileBytes < 1 {
		return cfg, fmt.Errorf("config %s: snapshots.max_file_bytes must be >= 1, got %d", src, cfg.Snapshots.MaxFileBytes)
	}
	if cfg.MaxImageBytes < 1 {
		return cfg, fmt.Errorf("config %s: max_image_bytes must be >= 1, got %d", src, cfg.MaxImageBytes)
	}
	if !ValidEnvironment(cfg.Environment) {
		return cfg, fmt.Errorf("config %s: environment %q must be local or container", src, cfg.Environment)
	}
	if err := cfg.CommandEnv.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.FileAccess.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.Permissions.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.RedactSecrets.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.VerifyOnStop.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.Memory.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.Handoff.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.OTel.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.WebFetch.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.UntrustedContent.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.Attempts.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := cfg.LSP.Validate(); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := validateAdditionalDirs(cfg.AdditionalDirs); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := validateMCPServers(cfg.MCPServers); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if err := validatePlugins(cfg.Plugins); err != nil {
		return cfg, &InvalidError{fmt.Errorf("config %s: %w", src, err)}
	}
	if r := cfg.Container.Runtime; r != "" && r != "docker" && r != "podman" {
		return cfg, fmt.Errorf("config %s: container.runtime %q must be docker or podman", src, r)
	}
	if err := validateProviders(cfg.Providers); err != nil {
		return cfg, fmt.Errorf("config %s: %w", src, err)
	}
	for _, p := range cfg.SandboxWritablePaths {
		if !filepath.IsAbs(p) {
			return cfg, fmt.Errorf("config %s: sandbox_writable_paths entry %q must be an absolute path", src, p)
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return cfg, fmt.Errorf("config %s: sandbox_writable_paths entry %q is not an existing directory", src, p)
		}
	}
	if _, err := sandboxnet.ParseRules(cfg.SandboxAllowedDomains); err != nil {
		return cfg, fmt.Errorf("config %s: sandbox_allowed_domains %v", src, err)
	}
	if !ValidSandboxBackend(cfg.SandboxBackend) {
		return cfg, fmt.Errorf("config %s: sandbox_backend %q must be auto, landlock or bwrap", src, cfg.SandboxBackend)
	}
	return cfg, nil
}

// checkRedactSecrets rejects unknown keys under redact_secrets and
// enabled or patterns values that aren't plain true or false.
func checkRedactSecrets(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i].Value, n.Content[i+1]
			if (k == "enabled" || k == "patterns") && v.ShortTag() != "!!bool" {
				return fmt.Errorf("redact_secrets.%s must be true or false, got %q", k, v.Value)
			}
		}
	}
	return checkStrict(n, "redact_secrets", &RedactSecrets{})
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

// validateProviders checks every providers entry. Errors wrap
// ErrInvalidProvider and name the offending entry.
func validateProviders(ps []Provider) error {
	seen := map[string]bool{}
	for i, p := range ps {
		where := fmt.Sprintf("providers[%d]", i)
		if p.ID != "" {
			where = fmt.Sprintf("providers[%d] (id %q)", i, p.ID)
		}
		bad := func(format string, args ...any) error {
			return fmt.Errorf("%w: %s: %s", ErrInvalidProvider, where, fmt.Sprintf(format, args...))
		}
		switch {
		case strings.TrimSpace(p.ID) == "":
			return bad("id is required")
		case strings.Contains(p.ID, "/"):
			return bad("id must not contain \"/\"")
		case p.ID != strings.TrimSpace(p.ID) || strings.ContainsAny(p.ID, " \t\r\n"):
			return bad("id must not contain whitespace")
		case slices.Contains(BuiltinProviders, p.ID):
			return bad("id %q is a built-in provider name (reserved: %s)", p.ID, strings.Join(BuiltinProviders, ", "))
		case seen[p.ID]:
			return bad("duplicate id %q", p.ID)
		}
		seen[p.ID] = true
		if !slices.Contains(ProviderAPIs, p.API) {
			return bad("api %q must be one of %s", p.API, strings.Join(ProviderAPIs, ", "))
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bad("base_url %q must be an absolute http or https URL", p.BaseURL)
		}
		if u.User != nil {
			return bad("base_url must not contain credentials; use api_key_env or env_headers")
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return bad("base_url must not have a query or fragment")
		}
		if p.APIKeyEnv != "" && !validEnvName(p.APIKeyEnv) {
			return bad("api_key_env %q is not a valid environment variable name", p.APIKeyEnv)
		}
		headers := map[string]bool{}
		for name, v := range p.Headers {
			if !validHeaderName(name) {
				return bad("header name %q is not a valid HTTP token", name)
			}
			if l := strings.ToLower(name); l == "authorization" || l == "x-api-key" || l == "api-key" {
				return bad("header %q carries a credential; set it with api_key_env or env_headers instead", name)
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				return bad("header %q value contains a control character", name)
			}
			headers[strings.ToLower(name)] = true
		}
		for name, env := range p.EnvHeaders {
			if !validHeaderName(name) {
				return bad("env_headers name %q is not a valid HTTP token", name)
			}
			if headers[strings.ToLower(name)] {
				return bad("header %q is set in both headers and env_headers", name)
			}
			if !validEnvName(env) {
				return bad("env_headers %q: %q is not a valid environment variable name", name, env)
			}
		}
		for name, m := range p.Models {
			if strings.TrimSpace(name) == "" {
				return bad("models has an empty model name")
			}
			for _, f := range []struct {
				key string
				v   float64
			}{{"input_price_per_mtok", m.InputPricePerMTok}, {"output_price_per_mtok", m.OutputPricePerMTok}} {
				if f.v < 0 || math.IsNaN(f.v) || math.IsInf(f.v, 0) {
					return bad("models[%q].%s must be a finite number >= 0, got %v", name, f.key, f.v)
				}
			}
			if c := m.CacheReadMultiplier; c < 0 || c > 1 || math.IsNaN(c) {
				return bad("models[%q].cache_read_multiplier must be between 0 and 1, got %v", name, c)
			}
			if m.ContextWindow < 0 {
				return bad("models[%q].context_window must be >= 0, got %d", name, m.ContextWindow)
			}
		}
	}
	return nil
}

// validHeaderName reports whether s is an RFC 9110 token.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", c):
		default:
			return false
		}
	}
	return true
}

func validEnvName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
