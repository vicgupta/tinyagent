package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

// Issue #429: where the model came from.

func TestResolveModelOriginSources(t *testing.T) {
	for _, c := range []struct {
		name, env, cfgModel string
		args                []string
		spec                string
		kind                modelOriginKind
		source              string
	}{
		{"flag beats env and config", "openai/gpt-6-luna", "codex-exec/gpt-6-luna", []string{"-m", "anthropic/claude-sonnet-4-5"}, "anthropic/claude-sonnet-4-5", originFlag, "-m/--model"},
		{"flag long form", "", "", []string{"--model", "ollama/llama3.1"}, "ollama/llama3.1", originFlag, "-m/--model"},
		{"env beats config", "openai/gpt-6-luna", "codex-exec/gpt-6-luna", nil, "openai/gpt-6-luna", originEnv, "$AGENT_MODEL"},
		{"config", "", "codex-exec/gpt-6-luna", nil, "codex-exec/gpt-6-luna", originConfig, "model: in ./.tinyagent/tinyagent.yaml"},
		{"default", "", "", nil, builtinModel, originDefault, "the built-in default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("AGENT_MODEL", c.env)
			var o options
			fs := newFlagSet(&o)
			if err := fs.Parse(c.args); err != nil {
				t.Fatal(err)
			}
			mo := resolveModelOrigin(fs, o.modelName, c.cfgModel, filepath.Join(".tinyagent", "tinyagent.yaml"))
			if mo.spec != c.spec || mo.kind != c.kind || mo.source() != filepath.FromSlash(c.source) {
				t.Errorf("got %q %v %q, want %q %v %q", mo.spec, mo.kind, mo.source(), c.spec, c.kind, c.source)
			}
			// The precedence is resolveModel's, unchanged.
			if want := resolveModel(fs, o.modelName, c.cfgModel); mo.spec != want {
				t.Errorf("spec %q, resolveModel %q", mo.spec, want)
			}
		})
	}
}

func TestModelOverrideNote(t *testing.T) {
	path := filepath.Join(".tinyagent", "tinyagent.yaml")
	env := modelOrigin{spec: "openai/gpt-6-luna", kind: originEnv, cfgModel: "codex-exec/gpt-6-luna", cfgPath: path}
	want := "note: using $AGENT_MODEL=openai/gpt-6-luna instead of model: codex-exec/gpt-6-luna in " + filepath.FromSlash("./.tinyagent/tinyagent.yaml") + " (unset AGENT_MODEL to use the file)"
	if got := env.overrideNote(); got != want {
		t.Errorf("note %q\nwant %q", got, want)
	}
	var b bytes.Buffer
	noteModelOverride(&b, env)
	if b.String() != want+"\n" {
		t.Errorf("printed %q", b.String())
	}
	for _, mo := range []modelOrigin{
		{spec: "openai/gpt-6-luna", kind: originEnv, cfgModel: "openai/gpt-6-luna", cfgPath: path}, // same model
		{spec: "openai/gpt-6-luna", kind: originEnv, cfgPath: path},                                // no model: in the file
		{spec: "openai/gpt-6-luna", kind: originFlag, cfgModel: "codex-exec/gpt-6-luna"},           // -m is explicit
		{spec: "codex-exec/gpt-6-luna", kind: originConfig, cfgModel: "codex-exec/gpt-6-luna"},
		{spec: builtinModel, kind: originDefault},
	} {
		if n := mo.overrideNote(); n != "" {
			t.Errorf("%+v: unexpected note %q", mo, n)
		}
	}
}

func TestModelOriginMessages(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	abs := filepath.Join(t.TempDir(), "c.yaml")
	env := modelOrigin{spec: "openai/gpt-6-luna", kind: originEnv, cfgModel: "codex-exec/gpt-6-luna", cfgPath: abs}
	if got, want := env.missingKey("OPENAI_API_KEY"), "OPENAI_API_KEY is not set for model openai/gpt-6-luna (from $AGENT_MODEL)"; got != want {
		t.Errorf("missingKey %q, want %q", got, want)
	}
	h := hintFrom(env, "HTTP 401: ")
	if !strings.HasPrefix(h, "OPENAI_API_KEY isn't set") || !strings.HasSuffix(h, "; the model openai/gpt-6-luna is from $AGENT_MODEL (unset AGENT_MODEL to use model: codex-exec/gpt-6-luna in "+abs+")") {
		t.Errorf("hint %q", h)
	}
	// Untracked: the messages are what they were before #429.
	plain := modelOrigin{spec: "openai/gpt-6-luna"}
	if got := plain.missingKey("OPENAI_API_KEY"); got != "OPENAI_API_KEY is not set" {
		t.Errorf("untracked missingKey %q", got)
	}
	if got, want := hintFrom(plain, "HTTP 401: "), "OPENAI_API_KEY isn't set: export OPENAI_API_KEY=... and rerun, e.g. tinyagent -m openai/gpt-6-luna -t \"...\""; got != want {
		t.Errorf("untracked hint %q", got)
	}
	if hintFrom(env, "some other error") != "" {
		t.Error("a source line without a hint")
	}
	// A model switched away from the resolved one is untracked.
	if mo := originFor(env, "anthropic/claude-sonnet-4-5"); mo.source() != "" || mo.spec != "anthropic/claude-sonnet-4-5" {
		t.Errorf("originFor switched: %+v", mo)
	}
	if mo := originFor(env, env.spec); mo != env {
		t.Errorf("originFor same: %+v", mo)
	}
	for _, c := range []struct {
		mo   modelOrigin
		want string
	}{
		{modelOrigin{kind: originConfig}, "model: in the config"},
		{modelOrigin{kind: originConfig, cfgPath: abs}, "model: in " + abs},
		{modelOrigin{kind: originConfig, cfgPath: filepath.Join("..", "c.yaml")}, "model: in " + filepath.Join("..", "c.yaml")},
		{modelOrigin{kind: originCheckpoint}, "the --resume checkpoint"},
	} {
		if got := c.mo.source(); got != c.want {
			t.Errorf("source %q, want %q", got, c.want)
		}
	}
	err := attemptModelError(1, "openai/gpt-6-luna", env, errors.New("OPENAI_API_KEY is not set"))
	if want := `attempt 1 model "openai/gpt-6-luna" (from $AGENT_MODEL): OPENAI_API_KEY is not set; unset AGENT_MODEL to use model: codex-exec/gpt-6-luna in ` + abs; err.Error() != want {
		t.Errorf("attempt error %q\nwant %q", err, want)
	}
}

// originEnvSetup isolates the environment and returns a config file whose
// model: is codex-exec/gpt-6-luna, with $AGENT_MODEL set to an OpenAI model.
func originEnvSetup(t *testing.T) string {
	t.Helper()
	checkEnv(t)
	t.Setenv("AGENT_MODEL", "openai/gpt-6-luna")
	return writeConfig(t, "model: codex-exec/gpt-6-luna\n")
}

func overrideNoteFor(cfg string) string {
	return "note: using $AGENT_MODEL=openai/gpt-6-luna instead of model: codex-exec/gpt-6-luna in " + cfg + " (unset AGENT_MODEL to use the file)"
}

func TestModelOriginTaskRun(t *testing.T) {
	cfg := originEnvSetup(t)
	dir := t.TempDir()
	t.Run("note", func(t *testing.T) {
		var requests atomic.Int32
		srv := fakeOpenAI(t, &requests)
		t.Setenv("OPENAI_BASE_URL", srv.URL)
		t.Setenv("OPENAI_API_KEY", "fake")
		code, _, stderr := runCLI(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg)
		if code != 0 || strings.Count(stderr, overrideNoteFor(cfg)) != 1 {
			t.Errorf("exit %d, want one note:\n%s", code, stderr)
		}
		if strings.Contains(stderr, "fake") {
			t.Errorf("key value printed:\n%s", stderr)
		}
	})
	t.Run("missing key hint names the source", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"message":"missing key"}}`, http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("OPENAI_BASE_URL", srv.URL)
		t.Setenv("OPENAI_API_KEY", "")
		_, _, stderr := runCLI(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg)
		if !strings.Contains(stderr, "hint: OPENAI_API_KEY isn't set") || !strings.Contains(stderr, "; the model openai/gpt-6-luna is from $AGENT_MODEL (unset AGENT_MODEL to use model: codex-exec/gpt-6-luna in "+cfg+")") {
			t.Errorf("hint without the source:\n%s", stderr)
		}
	})
	t.Run("-m: no note", func(t *testing.T) {
		var requests atomic.Int32
		srv := fakeOpenAI(t, &requests)
		t.Setenv("OPENAI_BASE_URL", srv.URL)
		t.Setenv("OPENAI_API_KEY", "fake")
		code, _, stderr := runCLI(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg, "-m", "openai/gpt-4o")
		if code != 0 || strings.Contains(stderr, "note: using $AGENT_MODEL") {
			t.Errorf("exit %d, unexpected note:\n%s", code, stderr)
		}
	})
}

func TestModelOriginCheck(t *testing.T) {
	cfg := originEnvSetup(t)
	t.Run("missing key", func(t *testing.T) {
		code, _, stderr := runCLI(t, "--check", "-c", cfg)
		want := "error: OPENAI_API_KEY is not set for model openai/gpt-6-luna (from $AGENT_MODEL)\nhint: OPENAI_API_KEY isn't set"
		if code != 2 || !strings.HasPrefix(stderr, want) || !strings.Contains(stderr, "unset AGENT_MODEL to use model: codex-exec/gpt-6-luna in "+cfg) {
			t.Errorf("exit %d:\n%s", code, stderr)
		}
	})
	t.Run("overrides line", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "fake")
		code, stdout, stderr := runCLI(t, "--check", "-c", cfg)
		if code != 0 {
			t.Fatalf("exit %d: %s%s", code, stdout, stderr)
		}
		if got := reportLine(t, stdout, "model"); got != "openai/gpt-6-luna (from $AGENT_MODEL)" {
			t.Errorf("model line %q", got)
		}
		if got, want := reportLine(t, stdout, "overrides"), "model: codex-exec/gpt-6-luna in "+cfg+" (unset AGENT_MODEL to use it)"; got != want {
			t.Errorf("overrides line %q, want %q", got, want)
		}
		_, stdout, _ = runCLI(t, "--check", "--json", "-c", cfg)
		var sum struct {
			Model checkModel `json:"model"`
		}
		if err := json.Unmarshal([]byte(stdout), &sum); err != nil {
			t.Fatal(err)
		}
		if sum.Model != (checkModel{Spec: "openai/gpt-6-luna", Source: "$AGENT_MODEL", Overrides: "codex-exec/gpt-6-luna"}) {
			t.Errorf("json model %+v", sum.Model)
		}
		if strings.Contains(stdout, "fake") {
			t.Error("key value printed")
		}
	})
	t.Run("no override", func(t *testing.T) {
		t.Setenv("AGENT_MODEL", "")
		t.Setenv("OPENAI_API_KEY", "fake")
		_, stdout, _ := runCLI(t, "--check", "-c", writeConfig(t, "model: openai/gpt-6-luna\n"))
		if strings.Contains(stdout, "overrides:") || strings.Contains(stdout, `"overrides"`) {
			t.Errorf("overrides without $AGENT_MODEL:\n%s", stdout)
		}
	})
}

// chat, web and ACP share the session setup.
func TestModelOriginSession(t *testing.T) {
	start := func(t *testing.T, args ...string) (int, string) {
		t.Helper()
		var o options
		fs := newFlagSet(&o)
		if err := fs.Parse(args); err != nil {
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
	t.Run("web: note and missing key", func(t *testing.T) {
		envTest(t, false)
		t.Setenv("AGENT_MODEL", "openai/gpt-6-luna")
		root := acpWorkspace(t)
		file := filepath.Join(root, config.ProjectFile)
		writeFile(t, file, "model: codex-exec/gpt-6-luna\n")
		code, out := start(t, "--web", "--cwd", root)
		if code == 0 || !strings.Contains(out, overrideNoteFor(file)) {
			t.Errorf("exit %d, no note:\n%s", code, out)
		}
		if !strings.Contains(out, "OPENAI_API_KEY is not set for model openai/gpt-6-luna (from $AGENT_MODEL); export it") ||
			!strings.Contains(out, "the model openai/gpt-6-luna is from $AGENT_MODEL (unset AGENT_MODEL to use model: codex-exec/gpt-6-luna in "+file+")") {
			t.Errorf("missing-key error without the source:\n%s", out)
		}
	})
	t.Run("chat: file model without $AGENT_MODEL", func(t *testing.T) {
		envTest(t, false)
		root := acpWorkspace(t)
		file := filepath.Join(root, config.ProjectFile)
		writeFile(t, file, "model: anthropic/claude-sonnet-4-5\n")
		code, out := start(t, "--cwd", root)
		if code == 0 || strings.Contains(out, "note: using $AGENT_MODEL") || !strings.Contains(out, "ANTHROPIC_API_KEY is not set for model anthropic/claude-sonnet-4-5 (from model: in "+file+")") {
			t.Errorf("exit %d:\n%s", code, out)
		}
	})
}

func TestModelOriginACP(t *testing.T) {
	envTest(t, false)
	t.Setenv("AGENT_MODEL", "openai/gpt-6-luna")
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1")
	root := acpWorkspace(t)
	file := filepath.Join(root, config.ProjectFile)
	writeFile(t, file, "model: codex-exec/gpt-6-luna\n")
	c := startACP(t)
	c.wait(c.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}))
	c.newSession(root)
	_, _, stderr := c.close()
	if strings.Count(stderr, overrideNoteFor(file)) != 1 {
		t.Errorf("want one note:\n%s", stderr)
	}
}

func TestModelOriginAttempts(t *testing.T) {
	cfg := originEnvSetup(t)
	t.Setenv("AGENT_MODEL", "anthropic/claude-sonnet-4-5")
	repo := evalRepo(t)
	code, _, stderr := runCLI(t, "--attempts", "2", "--attempt-check", "true", "-t", "MODE_PASS", "--cwd", repo, "--no-stream", "-c", cfg)
	want := `attempt 1 model "anthropic/claude-sonnet-4-5" (from $AGENT_MODEL): ANTHROPIC_API_KEY is not set; unset AGENT_MODEL to use model: codex-exec/gpt-6-luna in ` + cfg
	note := "note: using $AGENT_MODEL=anthropic/claude-sonnet-4-5 instead of model: codex-exec/gpt-6-luna in " + cfg
	if code != 2 || !strings.Contains(stderr, want) || !strings.Contains(stderr, note) {
		t.Errorf("exit %d:\n%s", code, stderr)
	}
}

func TestModelOriginTune(t *testing.T) {
	cfg := originEnvSetup(t)
	t.Setenv("OPENAI_API_KEY", "fake")
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"-c", cfg}); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		if _, name, err := tuneModel(config.AgentConfig{Model: "codex-exec/gpt-6-luna"}, fs, &o); err != nil || name != "openai/gpt-6-luna" {
			t.Errorf("tuneModel: %q %v", name, err)
		}
	})
	if strings.Count(stderr, overrideNoteFor(cfg)) != 1 {
		t.Errorf("want one note:\n%s", stderr)
	}
}
