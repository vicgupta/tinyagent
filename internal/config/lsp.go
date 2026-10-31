package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// LSP configures opt-in language servers. Servers are trusted executables
// configured only in -c or a trusted ./tinyagent.yaml (--trust-config), launched directly (no shell)
// with a minimal environment, like MCP servers.
type LSP struct {
	Enabled bool `yaml:"enabled"`
	// DiagnosticsAfterEdit appends new diagnostics to write_file, edit_file
	// and apply_patch results (default true). false keeps only the lsp tool.
	DiagnosticsAfterEdit bool `yaml:"diagnostics_after_edit"`
	// DiagnosticsWaitMS is the longest wait for diagnostics after an edit
	// (MinLSPWaitMS..MaxLSPWaitMS; default 3000).
	DiagnosticsWaitMS int `yaml:"diagnostics_wait_ms"`
	// MaxDiagnostics caps the diagnostics listed per file (1..200;
	// default 20).
	MaxDiagnostics int `yaml:"max_diagnostics"`
	// Severity is the lowest severity reported: error (default) or
	// warning (errors and warnings).
	Severity string      `yaml:"severity"`
	Servers  []LSPServer `yaml:"servers,omitempty"`
}

// LSPServer is one language server, used for files with its extensions.
type LSPServer struct {
	Name       string   `yaml:"name"`
	Command    string   `yaml:"command"`
	Args       []string `yaml:"args,omitempty"`
	Extensions []string `yaml:"extensions"`
	// Env names host environment variables to forward; each must be set.
	Env []string `yaml:"env,omitempty"`
	// InitializationOptions is passed to the server's initialize request.
	InitializationOptions map[string]any `yaml:"initialization_options,omitempty"`
	// StartupTimeoutSeconds bounds start and initialize (1..300; 0 means
	// DefaultLSPStartupTimeoutSeconds).
	StartupTimeoutSeconds int `yaml:"startup_timeout_seconds"`
}

// LSP defaults and limits.
const (
	DefaultLSPWaitMS                = 3000
	MinLSPWaitMS                    = 100
	MaxLSPWaitMS                    = 30000
	DefaultLSPMaxDiagnostics        = 20
	MaxLSPMaxDiagnostics            = 200
	DefaultLSPStartupTimeoutSeconds = 15
	MaxLSPStartupTimeoutSeconds     = 300
	MaxLSPServers                   = 8
)

// DefaultLSP is the lsp section's defaults: off, diagnostics after edits
// when on, 3000 ms wait, 20 diagnostics, errors only.
func DefaultLSP() LSP {
	return LSP{DiagnosticsAfterEdit: true, DiagnosticsWaitMS: DefaultLSPWaitMS, MaxDiagnostics: DefaultLSPMaxDiagnostics, Severity: "error"}
}

var lspName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Validate checks the limits and every server entry. Whether servers are
// configured at all is checked once the --lsp flag has been applied
// (CheckEnabled), and forwarded variables at startup (CheckEnv).
func (l LSP) Validate() error {
	if l.DiagnosticsWaitMS < MinLSPWaitMS || l.DiagnosticsWaitMS > MaxLSPWaitMS {
		return fmt.Errorf("lsp.diagnostics_wait_ms must be between %d and %d, got %d", MinLSPWaitMS, MaxLSPWaitMS, l.DiagnosticsWaitMS)
	}
	if l.MaxDiagnostics < 1 || l.MaxDiagnostics > MaxLSPMaxDiagnostics {
		return fmt.Errorf("lsp.max_diagnostics must be between 1 and %d, got %d", MaxLSPMaxDiagnostics, l.MaxDiagnostics)
	}
	if l.Severity != "error" && l.Severity != "warning" {
		return fmt.Errorf("lsp.severity %q must be error or warning", l.Severity)
	}
	if len(l.Servers) > MaxLSPServers {
		return fmt.Errorf("lsp.servers: at most %d servers are allowed, got %d", MaxLSPServers, len(l.Servers))
	}
	names := map[string]int{}
	exts := map[string]string{}
	for i, s := range l.Servers {
		entry := fmt.Sprintf("lsp.servers[%d]", i)
		if s.Name != "" {
			entry += fmt.Sprintf(" (%s)", s.Name)
		}
		if !lspName.MatchString(s.Name) {
			return fmt.Errorf("%s: name is required and must contain only letters, digits, _ or - (at most 64)", entry)
		}
		if j, ok := names[s.Name]; ok {
			return fmt.Errorf("%s: duplicate name %q (also lsp.servers[%d])", entry, s.Name, j)
		}
		names[s.Name] = i
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("%s: command is required", entry)
		}
		if len(s.Extensions) == 0 {
			return fmt.Errorf("%s: extensions is required (for example [\".go\"])", entry)
		}
		for _, e := range s.Extensions {
			if len(e) < 2 || e[0] != '.' || strings.ContainsAny(e, `/\ `) || strings.Count(e, ".") != 1 {
				return fmt.Errorf("%s: extension %q must look like .go", entry, e)
			}
			key := strings.ToLower(e)
			if other, ok := exts[key]; ok {
				return fmt.Errorf("%s: extension %s is already claimed by server %s", entry, e, other)
			}
			exts[key] = s.Name
		}
		seen := map[string]bool{}
		for _, k := range s.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") || seen[k] {
				return fmt.Errorf("%s: invalid or duplicate env variable name %q", entry, k)
			}
			seen[k] = true
		}
		if t := s.StartupTimeoutSeconds; t < 0 || t > MaxLSPStartupTimeoutSeconds {
			return fmt.Errorf("%s: startup_timeout_seconds must be between 1 and %d, got %d", entry, MaxLSPStartupTimeoutSeconds, t)
		}
	}
	return nil
}

// CheckEnabled reports an error when LSP is on without servers. file names
// the config file in the message ("" when there is none).
func (l LSP) CheckEnabled(file string) error {
	if !l.Enabled || len(l.Servers) > 0 {
		return nil
	}
	if file == "" {
		return errors.New("lsp is enabled (--lsp) but no config file defines lsp.servers; add them to the -c file, or to ./tinyagent.yaml and pass --trust-config")
	}
	return fmt.Errorf("config %s: lsp is enabled but lsp.servers is empty", file)
}

// CheckEnv reports a forwarded environment variable that is not set.
func (l LSP) CheckEnv(file string) error {
	for i, s := range l.Servers {
		for _, k := range s.Env {
			if _, ok := os.LookupEnv(k); !ok {
				return fmt.Errorf("config %s: lsp.servers[%d] (%s): forwarded environment variable %s is not set", file, i, s.Name, k)
			}
		}
	}
	return nil
}

// checkStrict decodes n into v rejecting unknown keys, so a typo under key
// is an invalid setting (exit status 2) rather than a parse failure.
func checkStrict(n *yaml.Node, key string, v any) error {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%s must be a mapping", key)
	}
	b, err := yaml.Marshal(n)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}
