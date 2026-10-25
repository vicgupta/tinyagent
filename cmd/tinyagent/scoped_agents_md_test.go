package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const responsesReadScoped = `{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"read_file","arguments":"{\"path\":\"pkg/a/x.go\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`

func scopedSetup(t *testing.T) (dir, emptyCfg string) {
	t.Helper()
	dir, emptyCfg = openaiAPISetup(t)
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"pkg/a/x.go": "package a\n", "pkg/a/AGENTS.md": "Use make test-a.\n"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, emptyCfg
}

func TestScopedAgentsMDFlagEndToEnd(t *testing.T) {
	dir, empty := scopedSetup(t)
	onCfg := filepath.Join(t.TempDir(), "on.yaml")
	if err := os.WriteFile(onCfg, []byte("scoped_agents_md: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-c", empty, "--scoped-agents-md"}, {"-c", onCfg}} {
		code, stdout, stderr, bodies := lspRun(t, dir, []string{responsesReadScoped, responsesSubmit}, append(args, "--json")...)
		if code != 0 || len(bodies) != 2 {
			t.Fatalf("%v: exit %d, %d requests; stderr:\n%s", args, code, len(bodies), stderr)
		}
		outs := strings.Join(toolOutputs(bodies[1]), "\n")
		if !strings.Contains(outs, "[AGENTS.md for pkg/a/ (17 bytes)") || !strings.Contains(outs, "Use make test-a.\n[end AGENTS.md for pkg/a/]") {
			t.Errorf("%v: read_file result lacks the block:\n%s", args, outs)
		}
		started := runStarted(t, stdout)
		if started["scoped_agents_md_enabled"] != true {
			t.Errorf("%v: run.started = %v", args, started)
		}
		if s, _ := json.Marshal(started["scoped_agents_md"]); string(s) != `[]` {
			t.Errorf("%v: run.started scoped_agents_md = %s", args, s)
		}
		loaded := jsonEvents(t, stdout, "instructions.loaded")
		if len(loaded) != 1 || loaded[0]["path"] != "pkg/a/AGENTS.md" || loaded[0]["scope_dir"] != "pkg/a/" || loaded[0]["tool"] != "read_file" || loaded[0]["tool_call_id"] != "c1" {
			t.Errorf("%v: instructions.loaded = %v", args, loaded)
		}
	}
}

func TestScopedAgentsMDOffByDefaultCLI(t *testing.T) {
	dir, empty := scopedSetup(t)
	onCfg := filepath.Join(t.TempDir(), "on.yaml")
	if err := os.WriteFile(onCfg, []byte("scoped_agents_md: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-c", empty}, {"-c", onCfg, "--scoped-agents-md=false"}} {
		code, stdout, stderr, bodies := lspRun(t, dir, []string{responsesReadScoped, responsesSubmit}, append(args, "--json")...)
		if code != 0 || len(bodies) != 2 {
			t.Fatalf("%v: exit %d; stderr:\n%s", args, code, stderr)
		}
		if outs := strings.Join(toolOutputs(bodies[1]), "\n"); strings.Contains(outs, "AGENTS.md") {
			t.Errorf("%v: delivered while off:\n%s", args, outs)
		}
		if strings.Contains(stdout, "scoped_agents_md") || strings.Contains(stdout, "instructions.") {
			t.Errorf("%v: events mention scoped AGENTS.md:\n%s", args, stdout)
		}
	}
}

func TestScopedAgentsMDWithNoAgentsMD(t *testing.T) {
	dir, empty := scopedSetup(t)
	code, _, stderr, bodies := lspRun(t, dir, []string{responsesSubmit}, "-c", empty, "--no-agents-md", "--scoped-agents-md")
	if code != 2 || len(bodies) != 0 || !strings.Contains(stderr, "--no-agents-md and --scoped-agents-md can't be combined") {
		t.Fatalf("exit %d, %d requests: %s", code, len(bodies), stderr)
	}
	// The config key yields to --no-agents-md.
	onCfg := filepath.Join(t.TempDir(), "on.yaml")
	if err := os.WriteFile(onCfg, []byte("scoped_agents_md: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr, bodies = lspRun(t, dir, []string{responsesReadScoped, responsesSubmit}, "-c", onCfg, "--no-agents-md")
	if code != 0 || len(bodies) != 2 {
		t.Fatalf("config + --no-agents-md: exit %d: %s", code, stderr)
	}
	if outs := strings.Join(toolOutputs(bodies[1]), "\n"); strings.Contains(outs, "AGENTS.md") {
		t.Errorf("delivered with --no-agents-md:\n%s", outs)
	}
}

func TestResumeScopedAgentsMDSettingMustMatch(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	f := &fakeResponses{replies: []string{responsesSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--scoped-agents-md")
	})
	if code != 2 || !strings.Contains(stderr, "--scoped-agents-md conflicts with the checkpoint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	tr.ScopedAgentsMDEnabled = true
	if err := tr.Save(source); err != nil {
		t.Fatal(err)
	}
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--scoped-agents-md=false")
	})
	if code != 2 || !strings.Contains(stderr, "--scoped-agents-md conflicts with the checkpoint") {
		t.Fatalf("--scoped-agents-md=false: exit %d: %s", code, stderr)
	}
	if len(f.bodies) != 0 {
		t.Errorf("%d model requests before the conflict error", len(f.bodies))
	}
	if _, err := os.Stat(output); err == nil {
		t.Error("conflicting resume wrote an output trajectory")
	}

	// Without the flag the checkpoint's setting wins over the config.
	var stdout string
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--openai-api", "responses", "--no-stream", "--json")
		})
	})
	if code != 0 || len(f.bodies) == 0 {
		t.Fatalf("resume without the flag: exit %d, %d requests: %s", code, len(f.bodies), stderr)
	}
	if started := runStarted(t, stdout); started["scoped_agents_md_enabled"] != true {
		t.Errorf("resumed run.started = %v", started)
	}
}
