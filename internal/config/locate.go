package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
)

// ProjectFile is the project config file, relative to --cwd: it lives with
// the rest of tinyagent's cwd state in .tinyagent/. It is looked up only in
// the --cwd directory itself, never in a parent.
const ProjectFile = ".tinyagent/tinyagent.yaml"

// LegacyProjectFile is the old project config location (./tinyagent.yaml in
// --cwd). It is never read; when it exists, Locate adds a note to move it.
const LegacyProjectFile = "tinyagent.yaml"

// SourceKind says where a config file came from.
type SourceKind int

const (
	// SourceNone means no file: the built-in defaults.
	SourceNone SourceKind = iota
	// SourceFlag is a file named with -c/--config.
	SourceFlag
	// SourceCwd is ./.tinyagent/tinyagent.yaml in the --cwd directory.
	SourceCwd
)

// Source is the config file Locate picked, plus one-line notes for stderr.
type Source struct {
	Path  string
	Kind  SourceKind
	Notes []string
}

// Label describes the source for --check and -v.
func (s Source) Label() string {
	switch s.Kind {
	case SourceFlag:
		return s.Path + " (from -c)"
	case SourceCwd:
		return s.Path + " (cwd)"
	}
	return "none (built-in defaults)"
}

// Locate returns the config file to load: explicit (-c) if set; else
// .tinyagent/tinyagent.yaml in cwd (exactly that directory, no walking up);
// else none. The old ./tinyagent.yaml is never read: when it exists (and no
// -c was given) a note says to move it. The old per-user config
// (DefaultPath) is never read either; when it exists and nothing else was
// found, a note points at tinyagent init --migrate. A cwd file that exists
// but can't be read is still returned, so Load reports it.
func Locate(explicit, cwd string) Source {
	if explicit != "" {
		return Source{Path: explicit, Kind: SourceFlag}
	}
	if cwd == "" {
		cwd = "."
	}
	var notes []string
	legacy := filepath.Join(cwd, LegacyProjectFile)
	if _, err := os.Lstat(legacy); err == nil {
		notes = append(notes, LegacyProjectHint(legacy))
	}
	p := filepath.Join(cwd, ProjectFile)
	// A .tinyagent that is a plain file (ENOTDIR) means no project config.
	if _, err := os.Stat(p); err == nil || !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return Source{Path: p, Kind: SourceCwd, Notes: notes}
	}
	if user := legacyUserConfig(); user != "" {
		notes = append(notes, fmt.Sprintf("note: user config %s is no longer read; run tinyagent init --migrate to copy it to ./%s", user, ProjectFile))
	}
	return Source{Notes: notes}
}

// LegacyProjectHint is the note for an old ./tinyagent.yaml at path.
func LegacyProjectHint(path string) string {
	return fmt.Sprintf("hint: project config now lives in ./%s; %s is no longer read (move it there to keep it: mkdir -p .tinyagent && mv tinyagent.yaml .tinyagent/)", ProjectFile, path)
}

// UntrustedKeys lists the settings a project (cwd) config may set only
// with --trust-config: the ones that make tinyagent launch an executable or
// shell command of its own, and the security policy a repository must not
// loosen (permission rules, file_access, command_env, redact_secrets,
// untrusted_content, memory, container run_args).
var UntrustedKeys = []string{
	"mcp_servers", "hooks", "command_tools", "lsp.servers", "plugins", "tinygo",
	"claude_code.path", "codex_exec.path", "opencode.path", "handoff.startup_command",
	"verify_on_stop.checks", "attempts.checks", "acp.client_mcp_servers",
	"container.run_args", "permissions", "file_access", "command_env",
	"redact_secrets", "untrusted_content", "memory",
}

// StripUntrusted resets the UntrustedKeys settings in cfg to their
// defaults and returns the ones that differed, in UntrustedKeys order.
func StripUntrusted(cfg *AgentConfig) []string {
	d := Default()
	var out []string
	reset := func(key string, field, def any) {
		if !reflect.DeepEqual(reflect.ValueOf(field).Elem().Interface(), def) {
			out = append(out, key)
			reflect.ValueOf(field).Elem().Set(reflect.ValueOf(def))
		}
	}
	reset("mcp_servers", &cfg.MCPServers, d.MCPServers)
	reset("hooks", &cfg.Hooks, d.Hooks)
	reset("command_tools", &cfg.CommandTools, d.CommandTools)
	reset("lsp.servers", &cfg.LSP.Servers, d.LSP.Servers)
	reset("plugins", &cfg.Plugins, d.Plugins)
	reset("tinygo", &cfg.TinyGo, d.TinyGo)
	reset("claude_code.path", &cfg.ClaudeCode.Path, d.ClaudeCode.Path)
	reset("codex_exec.path", &cfg.CodexExec.Path, d.CodexExec.Path)
	reset("opencode.path", &cfg.OpenCode.Path, d.OpenCode.Path)
	reset("handoff.startup_command", &cfg.Handoff.StartupCommand, d.Handoff.StartupCommand)
	reset("verify_on_stop.checks", &cfg.VerifyOnStop.Checks, d.VerifyOnStop.Checks)
	reset("attempts.checks", &cfg.Attempts.Checks, d.Attempts.Checks)
	reset("acp.client_mcp_servers", &cfg.ACP.ClientMCPServers, d.ACP.ClientMCPServers)
	reset("container.run_args", &cfg.Container.RunArgs, d.Container.RunArgs)
	reset("permissions", &cfg.Permissions, d.Permissions)
	reset("file_access", &cfg.FileAccess, d.FileAccess)
	reset("command_env", &cfg.CommandEnv, d.CommandEnv)
	reset("redact_secrets", &cfg.RedactSecrets, d.RedactSecrets)
	reset("untrusted_content", &cfg.UntrustedContent, d.UntrustedContent)
	reset("memory", &cfg.Memory, d.Memory)
	return out
}

// legacyUserConfig returns DefaultPath when something exists there, else "".
// It is only stat'ed, never read.
func legacyUserConfig() string {
	p, err := DefaultPath()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return p
}
