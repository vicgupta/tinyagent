package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadYAML(t *testing.T, body string) (AgentConfig, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	return cfg, path, err
}

func TestProvidersLoad(t *testing.T) {
	cfg, _, err := loadYAML(t, `
providers:
  - id: gateway
    api: anthropic
    base_url: https://gw.example.com
    api_key_env: GATEWAY_API_KEY
    headers: {X-Team: platform}
    env_headers: {X-Tenant: GATEWAY_TENANT}
    models:
      claude-x:
        input_price_per_mtok: 3
        output_price_per_mtok: 15
        cache_read_multiplier: 0.1
        context_window: 200000
  - id: local
    api: chat
    base_url: http://localhost:8080/v1
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 2 || cfg.Providers[0].Models["claude-x"].ContextWindow != 200000 || cfg.Providers[1].APIKeyEnv != "" {
		t.Errorf("providers = %+v", cfg.Providers)
	}
}

// TestProvidersInvalid covers each AC6 case: exit-2 class error naming the
// file and the entry.
func TestProvidersInvalid(t *testing.T) {
	ok := "    api: chat\n    base_url: http://localhost:1\n"
	for name, c := range map[string]struct{ yaml, want string }{
		"builtin id":        {"  - id: openai\n" + ok, "reserved"},
		"slash id":          {"  - id: a/b\n" + ok, `"/"`},
		"empty id":          {"  - id: \"\"\n" + ok, "id"},
		"duplicate":         {"  - id: gw\n" + ok + "  - id: gw\n" + ok, "duplicate"},
		"bad api":           {"  - id: gw\n    api: grpc\n    base_url: http://x\n", "api"},
		"ftp url":           {"  - id: gw\n    api: chat\n    base_url: ftp://x\n", "base_url"},
		"relative url":      {"  - id: gw\n    api: chat\n    base_url: relative/path\n", "base_url"},
		"userinfo url":      {"  - id: gw\n    api: chat\n    base_url: https://u:p@x\n", "base_url"},
		"negative price":    {"  - id: gw\n" + ok + "    models:\n      m: {input_price_per_mtok: -1}\n", "price"},
		"multiplier":        {"  - id: gw\n" + ok + "    models:\n      m: {cache_read_multiplier: 2}\n", "cache_read_multiplier"},
		"window":            {"  - id: gw\n" + ok + "    models:\n      m: {context_window: -5}\n", "context_window"},
		"bad env name":      {"  - id: gw\n" + ok + "    api_key_env: \"MY-KEY\"\n", "api_key_env"},
		"bad header":        {"  - id: gw\n" + ok + "    headers: {\"Bad Header\": x}\n", "header"},
		"credential header": {"  - id: gw\n" + ok + "    headers: {Authorization: \"Bearer x\"}\n", "credential"},
		"header in both":    {"  - id: gw\n" + ok + "    headers: {X-A: x}\n    env_headers: {X-A: ENV_A}\n", "X-A"},
	} {
		t.Run(name, func(t *testing.T) {
			_, path, err := loadYAML(t, "providers:\n"+c.yaml)
			if err == nil {
				t.Fatal("loaded")
			}
			if !errors.Is(err, ErrInvalidProvider) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "providers[") || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestProvidersUnknownKey(t *testing.T) {
	_, _, err := loadYAML(t, "providers:\n  - id: gw\n    api: chat\n    base_url: http://x\n    api_key: oops\n")
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Errorf("misspelled key: %v", err)
	}
	_, _, err = loadYAML(t, "providers:\n  - id: gw\n    api: chat\n    base_url: http://x\n    models:\n      m: {input_price: 1}\n")
	if err == nil || !strings.Contains(err.Error(), "input_price") {
		t.Errorf("misspelled model key: %v", err)
	}
}
