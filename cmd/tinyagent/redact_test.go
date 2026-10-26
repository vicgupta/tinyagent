package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestSecretRedactorSources(t *testing.T) { // AC3, AC4, AC10
	t.Setenv("GW_API_KEY", "provider-key-0001")
	t.Setenv("GW_HEADER", "provider-header-0002")
	t.Setenv("HOOK_CRED", "hook-cred-0003")
	cfg := config.Default()
	cfg.RedactSecrets = config.RedactSecrets{Enabled: true, Env: []string{"DATABASE_URL", "UNSET_*"}, Allow: []string{"PUBLIC_KEY"}}
	cfg.MCPServers = []config.MCPServer{{Alias: "s", Env: []string{"MCP_CRED=mcp-cred-0004"}}}
	cfg.Providers = []config.Provider{{ID: "gw", APIKeyEnv: "GW_API_KEY", EnvHeaders: map[string]string{"X-Auth": "GW_HEADER"}}}
	cfg.Hooks.PostTool = []config.Hook{{Command: "h", Env: []string{"HOOK_CRED"}}}
	environ := []string{
		"FOO_TOKEN=env-token-0005", "DATABASE_URL=postgres://u:db-pass-0006@h/db", "PUBLIC_KEY=public-0007",
		"SHORT_TOKEN=abc", "PLAIN=plain-value-0008", "OPENAI_API_KEY=openai-key-0009",
	}
	var w bytes.Buffer
	r, info := newSecretRedactor(cfg, environ, &w, true)
	in := strings.Join([]string{"provider-key-0001", "provider-header-0002", "hook-cred-0003", "mcp-cred-0004",
		"env-token-0005", "postgres://u:db-pass-0006@h/db", "openai-key-0009", "public-0007", "abc", "plain-value-0008"}, " ")
	got, n := r.RedactCount(in)
	if want := strings.Repeat("[REDACTED] ", 7) + "public-0007 abc plain-value-0008"; got != want || n != 7 {
		t.Errorf("redacted %d: %q", n, got)
	}
	wantNames := []string{"DATABASE_URL", "FOO_TOKEN", "GW_API_KEY", "GW_HEADER", "HOOK_CRED", "MCP_CRED", "OPENAI_API_KEY"}
	if info == nil || !info.Enabled || strings.Join(info.Names, ",") != strings.Join(wantNames, ",") {
		t.Errorf("info = %+v", info)
	}
	out := w.String()
	for _, want := range []string{"redact_secrets: 7 values from 7 variables\n", "masking DATABASE_URL, FOO_TOKEN,", "skipped SHORT_TOKEN (value shorter than 6 characters)", "env UNSET_* matched nothing"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q:\n%s", want, out)
		}
	}
	for _, v := range strings.Fields(in) {
		if strings.Contains(out, v) {
			t.Errorf("stderr has value %q", v)
		}
	}

	cfg.RedactSecrets.Enabled = false
	if r, info := newSecretRedactor(cfg, environ, &w, true); r != nil || info != nil {
		t.Error("redactor built while off")
	}
}

func TestRedactSecretsCLI(t *testing.T) { // AC1, AC2, AC11
	if runtime.GOOS == "windows" {
		t.Skip("runs echo through bash")
	}
	const secret = "s3cr3tvalue123"
	t.Setenv("FOO_TOKEN", secret)
	dir := t.TempDir()
	cfg := writeConfig(t, "redact_secrets:\n  enabled: true\nlog_db:\n  level: full\n")
	f := newEnvOpenAI(t, "sk-provider-sentinel-1234", `echo "token: $FOO_TOKEN"`)
	traj := filepath.Join(t.TempDir(), "run.json")
	db := filepath.Join(t.TempDir(), "log.db")
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg, "-o", traj, "--json", "--log-db="+db, "-v")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(f.bodies) != 2 || !strings.Contains(f.bodies[1], "token: [REDACTED]") {
		t.Fatalf("provider request lacks the masked result: %v", f.bodies)
	}
	logged, _ := os.ReadFile(db)
	b, _ := os.ReadFile(traj)
	for name, s := range map[string]string{"request": f.bodies[1], "stdout": stdout, "stderr": stderr, "trajectory": string(b), "log db": string(logged)} {
		if strings.Contains(s, secret) {
			t.Errorf("%s has the secret", name)
		}
	}
	if !strings.Contains(stderr, "redact_secrets: ") || !strings.Contains(stderr, "FOO_TOKEN") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if !strings.Contains(stdout, `"redactions":1`) || !strings.Contains(stdout, `"redact_secrets":{"enabled":true`) {
		t.Errorf("--json events lack redactions or run.started redact_secrets:\n%s", stdout)
	}
	var tr trajectory.Trajectory
	json.Unmarshal(b, &tr)
	if tr.RedactSecrets == nil || !tr.RedactSecrets.Enabled {
		t.Errorf("trajectory redact_secrets = %+v", tr.RedactSecrets)
	}

	// --redact-secrets=false turns the config setting off; so does leaving
	// it unset.
	for _, args := range [][]string{{"-c", cfg, "--redact-secrets=false"}, {}} {
		f = newEnvOpenAI(t, "sk-provider-sentinel-1234", `echo "token: $FOO_TOKEN"`)
		code, _, stderr = runCLI(t, append([]string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir}, args...)...)
		if code != 0 || !strings.Contains(f.bodies[1], "token: "+secret) || strings.Contains(stderr, "redact_secrets") {
			t.Errorf("%v: exit %d, stderr %s", args, code, stderr)
		}
	}
	// --redact-secrets turns it on without config.
	f = newEnvOpenAI(t, "sk-provider-sentinel-1234", `echo "token: $FOO_TOKEN"`)
	if code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--redact-secrets"); code != 0 || strings.Contains(f.bodies[1], secret) {
		t.Errorf("--redact-secrets: exit %d, %s", code, stderr)
	}
}

func TestRedactSecretsValidation(t *testing.T) { // AC11
	f := newEnvOpenAI(t, "k")
	dir := t.TempDir()
	for _, c := range []struct{ cfg, want string }{
		{"redact_secrets: {enabled: true, mask: [x]}\n", "mask"},
		{"redact_secrets: {env: [\"\"]}\n", "redact_secrets.env"},
		{"redact_secrets: {allow: [\"[\"]}\n", "redact_secrets.allow"},
		{"redact_secrets: {env: [A_URL], allow: [A_URL]}\n", "both env and allow"},
		{"redact_secrets: {patterns: maybe}\n", "redact_secrets.patterns"},
	} {
		code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", writeConfig(t, c.cfg))
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%q: exit %d, stderr %q", c.cfg, code, stderr)
		}
	}
	if len(f.bodies) != 0 {
		t.Errorf("%d provider requests", len(f.bodies))
	}
}
