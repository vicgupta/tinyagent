package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLSPConfig(t *testing.T) {
	d := Default().LSP
	if d.Enabled || !d.DiagnosticsAfterEdit || d.DiagnosticsWaitMS != 3000 || d.MaxDiagnostics != 20 || d.Severity != "error" || len(d.Servers) != 0 {
		t.Fatalf("defaults = %+v", d)
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, `lsp:
  enabled: true
  diagnostics_wait_ms: 500
  severity: warning
  servers:
    - name: go
      command: gopls
      args: [serve]
      extensions: [.go]
      env: [GOFLAGS]
      initialization_options: {staticcheck: true}
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := LSP{Enabled: true, DiagnosticsAfterEdit: true, DiagnosticsWaitMS: 500, MaxDiagnostics: 20, Severity: "warning", Servers: []LSPServer{{
		Name: "go", Command: "gopls", Args: []string{"serve"}, Extensions: []string{".go"}, Env: []string{"GOFLAGS"},
		InitializationOptions: map[string]any{"staticcheck": true},
	}}}
	if !reflect.DeepEqual(cfg.LSP, want) {
		t.Fatalf("got %+v\nwant %+v", cfg.LSP, want)
	}
	srv := "    - {name: go, command: gopls, extensions: [.go]}\n"
	for _, c := range []struct{ yaml, want string }{
		{"lsp: {diagnostics_wait_ms: 0}", "lsp.diagnostics_wait_ms must be between 100 and 30000, got 0"},
		{"lsp: {diagnostics_wait_ms: 30001}", "lsp.diagnostics_wait_ms"},
		{"lsp: {max_diagnostics: 0}", "lsp.max_diagnostics"},
		{"lsp: {max_diagnostics: 201}", "lsp.max_diagnostics"},
		{"lsp: {severity: info}", "lsp.severity"},
		{"lsp:\n  servers:\n    - {name: go, extensions: [.go]}", "lsp.servers[0] (go): command is required"},
		{"lsp:\n  servers:\n    - {name: go, command: gopls}", "lsp.servers[0] (go): extensions is required"},
		{"lsp:\n  servers:\n    - {name: go, command: gopls, extensions: [go]}", "lsp.servers[0] (go): extension"},
		{"lsp:\n  servers:\n" + srv + srv, "lsp.servers[1] (go): duplicate name"},
		{"lsp:\n  servers:\n" + srv + "    - {name: go2, command: gopls, extensions: [.GO]}", "lsp.servers[1] (go2): extension .GO is already claimed by server go"},
		{"lsp:\n  servers:\n    - {command: gopls, extensions: [.go]}", "lsp.servers[0]"},
		{"lsp:\n  servers:\n    - {name: go, command: gopls, extensions: [.go], env: [\"A=B\"]}", "env variable"},
		{"lsp:\n  servers:\n    - {name: go, command: gopls, extensions: [.go], startup_timeout_seconds: -1}", "startup_timeout_seconds"},
		{"lsp: {bogus: 1}", "bogus"},
		{"lsp:\n  servers:\n    - {name: go, command: gopls, extensions: [.go], bogus: 1}", "bogus"},
		{"lsp: [1]", "lsp must be a mapping"},
	} {
		write(t, p, c.yaml+"\n")
		_, err := Load(p)
		var invalid *InvalidError
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), p) || !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want InvalidError naming %s and %q", c.yaml, err, p, c.want)
		}
	}
	write(t, p, "lsp: {diagnostics_wait_ms: 30000, max_diagnostics: 200}\n")
	if _, err := Load(p); err != nil {
		t.Errorf("limits at the edge: %v", err)
	}
}

func TestLSPCheckEnabledAndEnv(t *testing.T) {
	if err := (LSP{Enabled: true}).CheckEnabled("/u/c.yaml"); err == nil || !strings.Contains(err.Error(), "/u/c.yaml") {
		t.Errorf("enabled without servers: %v", err)
	}
	if err := (LSP{Enabled: true}).CheckEnabled(""); err == nil || !strings.Contains(err.Error(), "lsp.servers") {
		t.Errorf("enabled without servers or file: %v", err)
	}
	if err := (LSP{}).CheckEnabled(""); err != nil {
		t.Errorf("disabled: %v", err)
	}
	t.Setenv("TINYAGENT_LSP_SET", "1")
	l := LSP{Servers: []LSPServer{{Name: "go", Env: []string{"TINYAGENT_LSP_SET"}}}}
	if err := l.CheckEnv("c.yaml"); err != nil {
		t.Errorf("set var: %v", err)
	}
	l.Servers[0].Env = append(l.Servers[0].Env, "TINYAGENT_LSP_UNSET_X")
	if err := l.CheckEnv("c.yaml"); err == nil || !strings.Contains(err.Error(), "TINYAGENT_LSP_UNSET_X is not set") || !strings.Contains(err.Error(), "lsp.servers[0] (go)") {
		t.Errorf("unset var: %v", err)
	}
}
