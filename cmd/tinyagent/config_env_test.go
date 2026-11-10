package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
)

// Sentinels for #410. None is a real key; they only reach the fakes.
const (
	yamlKey    = "sk-yaml-env-sentinel-7319"
	processKey = "sk-process-env-sentinel-2864"
	yamlCustom = "custom-yaml-sentinel-5521"
)

// keyedProvider is the ACP fake OpenAI server that also records each
// request's Authorization header.
type keyedProvider struct {
	*acpProvider
	mu   sync.Mutex
	auth []string
}

func (k *keyedProvider) lastAuth(t *testing.T) string {
	t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.auth) == 0 {
		t.Fatal("no model requests")
	}
	return k.auth[len(k.auth)-1]
}

// envTest clears the provider keys and HOME (checkEnv), forgets what an
// earlier test set from a config, and leaves TINYAGENT_ENV_TEST_CUSTOM
// unset. With a fake it points OPENAI_BASE_URL at it.
func envTest(t *testing.T, withFake bool, replies ...acpReply) *keyedProvider {
	t.Helper()
	checkEnv(t)
	t.Setenv("TINYAGENT_ENV_TEST_CUSTOM", "")
	resetConfigEnv := func() {
		configEnv.Lock()
		configEnv.set = map[string]string{}
		configEnv.Unlock()
	}
	resetConfigEnv()
	t.Cleanup(resetConfigEnv)
	if !withFake {
		return nil
	}
	k := &keyedProvider{acpProvider: &acpProvider{replies: replies, tokens: 100}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.auth = append(k.auth, r.Header.Get("Authorization"))
		k.mu.Unlock()
		k.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	return k
}

// envWorkspace is a workspace whose .tinyagent/tinyagent.yaml sets the
// OpenAI key and a custom variable under env:, plus extra lines.
func envWorkspace(t *testing.T, extra string) string {
	t.Helper()
	root := acpWorkspace(t)
	writeFile(t, filepath.Join(root, ".tinyagent", "tinyagent.yaml"),
		"model: openai/gpt-4o-mini\nenv:\n  OPENAI_API_KEY: \""+yamlKey+"\"\n  TINYAGENT_ENV_TEST_CUSTOM: \""+yamlCustom+"\"\n"+extra)
	return root
}

// #410: env: in .tinyagent/tinyagent.yaml supplies the provider key in every
// mode, and a key exported in the real environment wins over it.
func TestConfigEnvAcrossModes(t *testing.T) {
	modes := []struct {
		name string
		run  func(t *testing.T, root string)
	}{
		{"one-shot", func(t *testing.T, root string) {
			if code, _, stderr := runCLI(t, "-t", "hi", "--cwd", root, "--no-stream"); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
		}},
		{"chat", func(t *testing.T, root string) {
			if r := runChatTest(t, root, strings.NewReader("hello\n/exit\n"), nil); r.code != 0 {
				t.Fatalf("exit %d\n%s", r.code, r.stderr)
			}
		}},
		{"web", func(t *testing.T, root string) {
			c := startWebArgs(t, root)
			ev := c.events()
			c.post("/api/message", map[string]any{"text": "hello"})
			until(t, ev, "turn_end")
		}},
		{"acp", func(t *testing.T, root string) {
			c := startACP(t)
			c.wait(c.call("initialize", map[string]any{"protocolVersion": 1}))
			sess := c.newSession(root)
			if stop, r, _ := c.prompt(sess, "hi"); stop != "end_turn" {
				t.Fatalf("prompt: %q %v", stop, r)
			}
		}},
	}
	for _, m := range modes {
		t.Run(m.name+"/from config", func(t *testing.T) {
			k := envTest(t, true, acpReply{text: "ok\n" + agent.SubmitMarker})
			m.run(t, envWorkspace(t, ""))
			if got := k.lastAuth(t); got != "Bearer "+yamlKey {
				t.Errorf("Authorization = %q, want the env: key", got)
			}
			if got := os.Getenv("TINYAGENT_ENV_TEST_CUSTOM"); got != yamlCustom {
				t.Errorf("custom variable = %q", got)
			}
			if got := lastModel(t, k.acpProvider); got != "gpt-4o-mini" {
				t.Errorf("model = %v", got)
			}
		})
		t.Run(m.name+"/process wins", func(t *testing.T) {
			k := envTest(t, true, acpReply{text: "ok\n" + agent.SubmitMarker})
			t.Setenv("OPENAI_API_KEY", processKey)
			m.run(t, envWorkspace(t, ""))
			if got := k.lastAuth(t); got != "Bearer "+processKey {
				t.Errorf("Authorization = %q, want the exported key", got)
			}
			if got := os.Getenv("OPENAI_API_KEY"); got != processKey {
				t.Errorf("exported key replaced")
			}
		})
	}
}

// #410 acceptance: bare chat, web and ACP start in a cwd whose yaml sets
// OPENAI_API_KEY under env:, with nothing exported (no OPENAI_BASE_URL
// either, so the missing-key check really runs). No request is made.
func TestConfigEnvKeyCheck(t *testing.T) {
	t.Run("web", func(t *testing.T) {
		envTest(t, false)
		startWebArgs(t, envWorkspace(t, ""))
	})
	t.Run("chat", func(t *testing.T) {
		envTest(t, false)
		if r := runChatTest(t, envWorkspace(t, ""), strings.NewReader("/exit\n"), nil); r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
	})
	t.Run("acp", func(t *testing.T) {
		envTest(t, false)
		c := startACP(t)
		c.wait(c.call("initialize", map[string]any{"protocolVersion": 1}))
		c.newSession(envWorkspace(t, ""))
	})
	t.Run("check", func(t *testing.T) {
		envTest(t, false)
		code, stdout, stderr := runCLI(t, "--check", "--cwd", envWorkspace(t, ""))
		if code != 0 {
			t.Fatalf("exit %d: %s%s", code, stdout, stderr)
		}
		if got := reportLine(t, stdout, "env"); got != "OPENAI_API_KEY (from config), TINYAGENT_ENV_TEST_CUSTOM (from config)" {
			t.Errorf("env line %q", got)
		}
	})
	t.Run("without env: web names the key", func(t *testing.T) {
		envTest(t, false)
		root := acpWorkspace(t)
		var o options
		fs := newFlagSet(&o)
		if err := fs.Parse([]string{"--web", "--cwd", root}); err != nil {
			t.Fatal(err)
		}
		var errw bytes.Buffer
		var code int
		stderr := captureStderr(t, func() { _, _, code = newChatSession(&o, fs, &errw) })
		if out := errw.String() + stderr; code == 0 || !strings.Contains(out, "OPENAI_API_KEY is not set") || !strings.Contains(out, "env: in ./.tinyagent/tinyagent.yaml") {
			t.Errorf("exit %d: %s", code, out)
		}
	})
}

// #410 acceptance: with a non-OpenAI model: in the yaml, web never asks for
// OPENAI_API_KEY; it uses (or names) that provider's key.
func TestConfigEnvNonOpenAIModel(t *testing.T) {
	start := func(t *testing.T, yaml string) (int, string) {
		t.Helper()
		root := acpWorkspace(t)
		writeFile(t, filepath.Join(root, ".tinyagent", "tinyagent.yaml"), yaml)
		var o options
		fs := newFlagSet(&o)
		if err := fs.Parse([]string{"--web", "--cwd", root}); err != nil {
			t.Fatal(err)
		}
		var errw bytes.Buffer
		var code int
		stderr := captureStderr(t, func() {
			var s *session
			if s, _, code = newChatSession(&o, fs, &errw); s != nil {
				s.Close()
			}
		})
		return code, errw.String() + stderr
	}
	t.Run("key in env:", func(t *testing.T) {
		envTest(t, false)
		code, errs := start(t, "model: anthropic/claude-sonnet-4-5\nenv:\n  ANTHROPIC_API_KEY: \""+yamlKey+"\"\n")
		if code != 0 || strings.Contains(errs, "OPENAI_API_KEY") {
			t.Errorf("exit %d: %s", code, errs)
		}
	})
	t.Run("no key", func(t *testing.T) {
		envTest(t, false)
		code, errs := start(t, "model: anthropic/claude-sonnet-4-5\n")
		if code == 0 || !strings.Contains(errs, "ANTHROPIC_API_KEY is not set") || strings.Contains(errs, "OPENAI_API_KEY") {
			t.Errorf("exit %d: %s", code, errs)
		}
	})
	t.Run("ollama", func(t *testing.T) {
		envTest(t, false)
		code, errs := start(t, "model: ollama/llama3.1\n")
		if code != 0 || strings.Contains(errs, "OPENAI_API_KEY") {
			t.Errorf("exit %d: %s", code, errs)
		}
	})
}

// #410: env: values never show up in stderr, -v, --check, --json or a saved
// trajectory; names may.
func TestConfigEnvValuesNeverPrinted(t *testing.T) {
	leaks := func(t *testing.T, where, s string) {
		t.Helper()
		for _, v := range []string{yamlKey, yamlCustom, processKey} {
			if strings.Contains(s, v) {
				t.Errorf("%s contains an env: value:\n%s", where, s)
			}
		}
	}
	envTest(t, true, acpReply{text: "ok\n" + agent.SubmitMarker}, acpReply{text: "ok\n" + agent.SubmitMarker})
	root := envWorkspace(t, "")
	traj := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-t", "hi", "--cwd", root, "--no-stream", "-v", "-o", traj)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "env: set from config: OPENAI_API_KEY, TINYAGENT_ENV_TEST_CUSTOM") {
		t.Errorf("-v has no env names:\n%s", stderr)
	}
	leaks(t, "one-shot -v", stdout+stderr)
	b, err := os.ReadFile(traj)
	if err != nil {
		t.Fatal(err)
	}
	leaks(t, "trajectory", string(b))

	code, stdout, stderr = runCLI(t, "-t", "hi", "--cwd", root, "--json")
	if code != 0 {
		t.Fatalf("--json exit %d: %s", code, stderr)
	}
	leaks(t, "--json", stdout+stderr)

	for _, args := range [][]string{{"--check", "-v"}, {"--check", "--json"}} {
		code, stdout, stderr := runCLI(t, append(args, "--cwd", root)...)
		if code != 0 {
			t.Fatalf("%v exit %d: %s", args, code, stderr)
		}
		leaks(t, strings.Join(args, " "), stdout+stderr)
	}

	// An invalid value is reported by name only.
	writeFile(t, filepath.Join(root, ".tinyagent", "tinyagent.yaml"), "env:\n  SECRET_NUMBER: 98765432123\n")
	code, stdout, stderr = runCLI(t, "--check", "--cwd", root)
	if code == 0 || !strings.Contains(stderr, "env SECRET_NUMBER must be a string") || strings.Contains(stdout+stderr, "98765432123") {
		t.Errorf("exit %d: %s%s", code, stdout, stderr)
	}
}

// #410: env: is allowed from an untrusted cwd config (local API keys), but
// the variables that change which program runs (PATH, LD_*, GIT_*, ...)
// need --trust-config; a -c file is trusted.
func TestConfigEnvTrust(t *testing.T) {
	const yaml = "env:\n  OPENAI_API_KEY: \"" + yamlKey + "\"\n  GIT_SSH_COMMAND: \"evil-ssh\"\n  NODE_OPTIONS: \"--require=/evil.js\"\n"
	setup := func(t *testing.T) string {
		envTest(t, false)
		t.Setenv("GIT_SSH_COMMAND", "")
		t.Setenv("NODE_OPTIONS", "")
		root := acpWorkspace(t)
		writeFile(t, filepath.Join(root, ".tinyagent", "tinyagent.yaml"), yaml)
		return root
	}
	t.Run("untrusted", func(t *testing.T) {
		root := setup(t)
		var r chatRun
		stderr := captureStderr(t, func() { r = runChatTest(t, root, strings.NewReader("/exit\n"), nil, "-v") })
		stdout := r.out
		stderr += r.stderr
		if r.code != 0 {
			t.Fatalf("exit %d: %s", r.code, stderr)
		}
		if os.Getenv("OPENAI_API_KEY") != yamlKey {
			t.Error("OPENAI_API_KEY not set from an untrusted cwd config")
		}
		if os.Getenv("GIT_SSH_COMMAND") != "" || os.Getenv("NODE_OPTIONS") != "" {
			t.Error("launch variables set without --trust-config")
		}
		if !strings.Contains(stderr, "warning: ignoring env.GIT_SSH_COMMAND, env.NODE_OPTIONS from") || !strings.Contains(stderr, "--trust-config") {
			t.Errorf("no warning naming the dropped variables:\n%s", stderr)
		}
		if strings.Contains(stdout+stderr, "evil") {
			t.Errorf("value printed:\n%s%s", stdout, stderr)
		}
	})
	t.Run("trust-config", func(t *testing.T) {
		root := setup(t)
		if code, _, stderr := runCLI(t, "--check", "--cwd", root, "--trust-config"); code != 0 || strings.Contains(stderr, "ignoring") {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if os.Getenv("GIT_SSH_COMMAND") != "evil-ssh" || os.Getenv("NODE_OPTIONS") == "" {
			t.Error("launch variables not set with --trust-config")
		}
	})
	t.Run("-c", func(t *testing.T) {
		setup(t)
		if code, _, stderr := runCLI(t, "--check", "-c", writeConfig(t, yaml)); code != 0 || strings.Contains(stderr, "ignoring") {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if os.Getenv("GIT_SSH_COMMAND") != "evil-ssh" {
			t.Error("launch variable not set from -c")
		}
	})
}

// #410: a later load (another ACP session's project) replaces what an
// earlier config set, unsets names it no longer has, and never touches a
// variable the user exported.
func TestApplyConfigEnv(t *testing.T) {
	envTest(t, false)
	t.Setenv("TINYAGENT_ENV_TEST_A", "")
	t.Setenv("TINYAGENT_ENV_TEST_B", "exported")
	r := applyConfigEnv(map[string]string{"TINYAGENT_ENV_TEST_A": "a1", "TINYAGENT_ENV_TEST_B": "b1"})
	if strings.Join(r.fromConfig, ",") != "TINYAGENT_ENV_TEST_A" || strings.Join(r.kept, ",") != "TINYAGENT_ENV_TEST_B" {
		t.Fatalf("result = %+v", r)
	}
	if os.Getenv("TINYAGENT_ENV_TEST_A") != "a1" || os.Getenv("TINYAGENT_ENV_TEST_B") != "exported" {
		t.Fatal("first load")
	}
	applyConfigEnv(map[string]string{"TINYAGENT_ENV_TEST_A": "a2"})
	if os.Getenv("TINYAGENT_ENV_TEST_A") != "a2" {
		t.Error("a second config did not replace a value the first one set")
	}
	applyConfigEnv(nil)
	if _, ok := os.LookupEnv("TINYAGENT_ENV_TEST_A"); ok {
		t.Error("a name the new config dropped was not unset")
	}
	if os.Getenv("TINYAGENT_ENV_TEST_B") != "exported" {
		t.Error("exported variable changed")
	}
}

// #410: redact_secrets masks env: values like the other config secrets.
func TestConfigEnvSecretsRedacted(t *testing.T) {
	envTest(t, false)
	t.Setenv("TINYAGENT_ENV_TEST_CUSTOM", yamlCustom)
	found := false
	for _, s := range configSecrets(config.AgentConfig{Env: map[string]string{"TINYAGENT_ENV_TEST_CUSTOM": "unused"}}) {
		found = found || s.name == "TINYAGENT_ENV_TEST_CUSTOM" && s.value == yamlCustom
	}
	if !found {
		t.Error("env: variable not in configSecrets")
	}
}

// #410: the init starter shows model: and a commented env: example that,
// uncommented, sets OPENAI_API_KEY.
func TestStarterConfigEnvBlock(t *testing.T) {
	if !strings.Contains(starterConfig, "\n# model: ") || !strings.Contains(starterConfig, "\n# env:") {
		t.Fatal("starter config has no model: or env: example")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(starterConfig, "\n# ", "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Env["OPENAI_API_KEY"]; !ok || len(cfg.Env) != 1 {
		t.Errorf("env names = %v", config.EnvNames(cfg.Env))
	}
}
