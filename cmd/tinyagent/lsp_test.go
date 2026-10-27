package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lsp/lsptest"
	"github.com/vicgupta/tinyagent/internal/tools"
)

func TestMain(m *testing.M) {
	lsptest.Run()
	if len(os.Args) > 1 && os.Args[1] == tools.SandboxHelperArg {
		os.Exit(tools.RunSandboxHelper(os.Args[2:]))
	}
	if os.Getenv(evalChildEnv) == "1" { // a trial run by --eval in eval_test.go
		os.Exit(run())
	}
	os.Exit(m.Run())
}

// writeLSPConfig writes a config file with these servers and settings.
func writeLSPConfig(t *testing.T, dir, name string, enabled bool, servers ...config.LSPServer) string {
	t.Helper()
	l := config.DefaultLSP()
	l.Enabled, l.Servers = enabled, servers
	b, err := yaml.Marshal(map[string]any{"lsp": l})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const responsesWriteGo = `{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"write_file","arguments":"{\"path\":\"a.go\",\"content\":\"package a\\n// ERR: undefined: x\\n\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`

// lspRun runs a task against a fake Responses server, returning the exit
// code, stdout, stderr (flag output included) and the request bodies.
func lspRun(t *testing.T, dir string, replies []string, args ...string) (int, string, string, []map[string]any) {
	t.Helper()
	f := &fakeResponses{replies: replies}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	var code int
	var stdout, errOut string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, errOut = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--openai-api", "responses", "--no-stream"}, args...)...)
		})
	})
	return code, stdout, stderr + errOut, f.bodies
}

// toolOutputs returns the function_call_output texts sent in body.
func toolOutputs(body map[string]any) []string {
	var outs []string
	items, _ := body["input"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["type"] == "function_call_output" {
			s, _ := m["output"].(string)
			outs = append(outs, s)
		}
	}
	return outs
}

// TestLSPFlagEndToEnd covers AC1 and AC3 through the CLI: --lsp starts the
// configured server, write_file's result carries its diagnostics, the lsp
// tool is offered, and run.started and lsp.server events record it.
func TestLSPFlagEndToEnd(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	cfg := writeLSPConfig(t, t.TempDir(), "lsp.yaml", false, lsptest.Server(t, "fake", []string{".go"}))
	code, stdout, stderr, bodies := lspRun(t, dir, []string{responsesWriteGo, responsesSubmit}, "-c", cfg, "--lsp", "--json")
	if code != 0 || len(bodies) != 2 {
		t.Fatalf("exit %d, %d requests; stderr:\n%s", code, len(bodies), stderr)
	}
	if names := sentToolNames(bodies[0]); !slices.Contains(names, "lsp") {
		t.Errorf("lsp tool not sent: %v", names)
	}
	outs := strings.Join(toolOutputs(bodies[1]), "\n")
	if !strings.Contains(outs, "[lsp fake: 1 error in a.go]") || !strings.Contains(outs, "a.go:2:4: undefined: x") {
		t.Errorf("write_file result lacks diagnostics:\n%s", outs)
	}
	if !strings.Contains(stderr, "lsp: fake (.go)") {
		t.Errorf("stderr lacks the server line:\n%s", stderr)
	}
	started := runStarted(t, stdout)
	l, _ := started["lsp"].(map[string]any)
	if s, _ := json.Marshal(l["servers"]); string(s) != `["fake"]` {
		t.Errorf("run.started lsp = %v", started["lsp"])
	}
	if !strings.Contains(stdout, `"lsp.server"`) || !strings.Contains(stdout, `"started"`) {
		t.Errorf("no lsp.server started event:\n%s", stdout)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, ".tinyagent", "lsp")); len(entries) != 1 {
		t.Errorf("want one server log, got %d", len(entries))
	}
}

// TestLSPOffByDefault covers AC1's default: no flag, no tool, no server.
func TestLSPOffByDefault(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	logf := filepath.Join(t.TempDir(), "log")
	cfg := writeLSPConfig(t, t.TempDir(), "lsp.yaml", false, lsptest.Server(t, "fake", []string{".go"}, "log="+logf))
	for _, args := range [][]string{{"-c", cfg}, {"-c", writeLSPConfig(t, t.TempDir(), "on.yaml", true, lsptest.Server(t, "fake", []string{".go"}, "log="+logf)), "--lsp=false"}} {
		code, _, stderr, bodies := lspRun(t, dir, []string{responsesWriteGo, responsesSubmit}, args...)
		if code != 0 || len(bodies) != 2 {
			t.Fatalf("%v: exit %d; stderr:\n%s", args, code, stderr)
		}
		if names := sentToolNames(bodies[0]); slices.Contains(names, "lsp") {
			t.Errorf("%v: lsp tool sent while off", args)
		}
		if outs := strings.Join(toolOutputs(bodies[1]), "\n"); strings.Contains(outs, "[lsp") {
			t.Errorf("%v: diagnostics while off:\n%s", args, outs)
		}
		if _, err := os.Stat(logf); err == nil {
			t.Errorf("%v: server started while off", args)
		}
	}
}

// TestLSPInvalidSettingsExit2 covers AC2 and AC15's unset variable: bad
// settings exit 2 before any provider request.
func TestLSPInvalidSettingsExit2(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	cdir := t.TempDir()
	srv := lsptest.Server(t, "fake", []string{".go"})
	noName := srv
	noName.Name = ""
	envSrv := srv
	envSrv.Env = []string{"TINYAGENT_LSP_TEST_UNSET"}
	os.Unsetenv("TINYAGENT_LSP_TEST_UNSET")
	badCfg := filepath.Join(cdir, "bad.yaml")
	os.WriteFile(badCfg, []byte("lsp:\n  enabled: true\n  bogus: 1\n"), 0o644)
	okCfg := writeLSPConfig(t, cdir, "ok.yaml", true, srv)
	for _, c := range []struct {
		name, want string
		args       []string
	}{
		{"flag without servers", "lsp.servers is empty", []string{"-c", emptyCfg, "--lsp"}},
		{"unknown key", "bogus", []string{"-c", badCfg}},
		{"no name", "lsp.servers[0]", []string{"-c", writeLSPConfig(t, cdir, "noname.yaml", true, noName)}},
		{"unset env", "TINYAGENT_LSP_TEST_UNSET is not set", []string{"-c", writeLSPConfig(t, cdir, "env.yaml", true, envSrv)}},
		{"sandbox", "--sandbox or --env container", []string{"-c", okCfg, "--sandbox"}},
		{"container", "--sandbox or --env container", []string{"-c", okCfg, "--env", "container"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr, bodies := lspRun(t, dir, []string{responsesSubmit}, c.args...)
			if code != 2 || !strings.Contains(stderr, c.want) || len(bodies) != 0 {
				t.Errorf("exit %d, %d requests, stderr:\n%s", code, len(bodies), stderr)
			}
		})
	}
}

// TestLSPRepoConfigIgnoredAndEnvMinimal covers AC15: settings come only
// from the user config or -c, and a server sees only PATH (plus the
// platform basics) and the variables it names.
func TestLSPRepoConfigIgnoredAndEnvMinimal(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	repoLog := filepath.Join(t.TempDir(), "repo-log")
	os.MkdirAll(filepath.Join(dir, ".tinyagent"), 0o755)
	writeLSPConfig(t, filepath.Join(dir, ".tinyagent"), "config.yaml", true, lsptest.Server(t, "repo", []string{".go"}, "log="+repoLog))
	writeLSPConfig(t, dir, ".tinyagent.yaml", true, lsptest.Server(t, "repo", []string{".go"}, "log="+repoLog))
	code, _, stderr, bodies := lspRun(t, dir, []string{responsesWriteGo, responsesSubmit}, "-c", emptyCfg)
	if code != 0 || len(bodies) != 2 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if _, err := os.Stat(repoLog); err == nil {
		t.Error("a server from a repository config file started")
	}

	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("TINYAGENT_LSP_SECRET", "s3cret")
	t.Setenv("TINYAGENT_LSP_FORWARD", "fwd")
	srv := lsptest.Server(t, "fake", []string{".go"}, "envfile="+envFile)
	srv.Env = []string{"TINYAGENT_LSP_FORWARD"}
	cfg := writeLSPConfig(t, t.TempDir(), "env.yaml", true, srv)
	if code, _, stderr, _ := lspRun(t, dir, []string{responsesWriteGo, responsesSubmit}, "-c", cfg); code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "s3cret") || strings.Contains(string(env), "OPENAI_API_KEY") || !strings.Contains(string(env), "TINYAGENT_LSP_FORWARD=fwd") {
		t.Errorf("server environment:\n%s", env)
	}
}

// TestLSPMissingBinaryWarns covers AC11 through the CLI: the run goes on
// and finishes with a warning.
func TestLSPMissingBinaryWarns(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	srv := lsptest.Server(t, "ghost", []string{".go"})
	srv.Command = filepath.Join(t.TempDir(), "no-such-language-server")
	cfg := writeLSPConfig(t, t.TempDir(), "lsp.yaml", true, srv)
	code, _, stderr, bodies := lspRun(t, dir, []string{responsesWriteGo, responsesSubmit}, "-c", cfg)
	if code != 0 || len(bodies) != 2 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "warning: lsp server ghost is unavailable for this run") {
		t.Errorf("no warning:\n%s", stderr)
	}
	if outs := strings.Join(toolOutputs(bodies[1]), "\n"); !strings.Contains(outs, "[lsp ghost: server unavailable; no diagnostics for a.go]") {
		t.Errorf("write_file result:\n%s", outs)
	}
}

// TestResumeLSPSettingMustMatch covers AC10's CLI conflicts.
func TestResumeLSPSettingMustMatch(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--lsp")
	})
	if code != 2 || !strings.Contains(stderr, "--lsp conflicts with the checkpoint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	tr.LSPEnabled = true
	if err := tr.Save(source); err != nil {
		t.Fatal(err)
	}
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--lsp=false")
	})
	if code != 2 || !strings.Contains(stderr, "--lsp conflicts with the checkpoint") {
		t.Fatalf("--lsp=false: exit %d: %s", code, stderr)
	}
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output)
	})
	if code != 2 || !strings.Contains(stderr, "resume: config "+cfg+": lsp is enabled but lsp.servers is empty") {
		t.Fatalf("no servers: exit %d: %s", code, stderr)
	}
}
