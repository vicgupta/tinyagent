package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// additionalDirsSetup returns a run directory, an empty config and two
// directories outside it.
func additionalDirsSetup(t *testing.T) (dir, emptyCfg, ref, out string) {
	t.Helper()
	dir, emptyCfg = openaiAPISetup(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, out = filepath.Join(base, "ref"), filepath.Join(base, "out")
	os.Mkdir(ref, 0o755)
	os.Mkdir(out, 0o755)
	return dir, emptyCfg, ref, out
}

func TestAdditionalDirsFlagsAndConfig(t *testing.T) {
	dir, emptyCfg, ref, out := additionalDirsSetup(t)
	cfgPath := filepath.Join(dir, "dirs.yaml")
	os.WriteFile(cfgPath, []byte("additional_dirs:\n  - {path: "+ref+", mode: read}\n  - {path: "+out+", mode: write}\n"), 0o644)
	for _, c := range []struct {
		name string
		args []string
		want []trajectory.AdditionalDir
	}{
		{"default", []string{"-c", emptyCfg}, nil},
		{"config", []string{"-c", cfgPath}, []trajectory.AdditionalDir{{Path: ref, Mode: "read"}, {Path: out, Mode: "write"}}},
		{"flags replace config", []string{"-c", cfgPath, "--add-dir-write", ref}, []trajectory.AdditionalDir{{Path: ref, Mode: "write"}}},
		{"flags", []string{"-c", emptyCfg, "--add-dir-read", ref, "--add-dir-write", out}, []trajectory.AdditionalDir{{Path: ref, Mode: "read"}, {Path: out, Mode: "write"}}},
		{"read-only forces read", []string{"-c", cfgPath, "--read-only"}, []trajectory.AdditionalDir{{Path: ref, Mode: "read"}, {Path: out, Mode: "read"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResponses{replies: []string{responsesSubmit}}
			t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
			outPath := filepath.Join(t.TempDir(), "run.json")
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--openai-api", "responses", "--no-stream", "-v", "-o", outPath}, c.args...)...)
			})
			if code != 0 || len(f.bodies) != 1 {
				t.Fatalf("exit %d, %d requests; stderr:\n%s", code, len(f.bodies), stderr)
			}
			body, _ := json.Marshal(f.bodies[0])
			tr, err := trajectory.Load(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.want) == 0 {
				if tr.AdditionalDirs != nil || strings.Contains(string(body), "additional_directories") || strings.Contains(stderr, "additional dir") {
					t.Errorf("additional dirs reported while off: %v\n%s", tr.AdditionalDirs, stderr)
				}
				return
			}
			if !slices.Equal(tr.AdditionalDirs, c.want) {
				t.Errorf("trajectory additional_dirs = %v, want %v", tr.AdditionalDirs, c.want)
			}
			for _, d := range c.want {
				line := "additional dir: " + d.Path + " (" + d.Mode + ")"
				if !strings.Contains(stderr, line) {
					t.Errorf("stderr lacks %q:\n%s", line, stderr)
				}
				if !strings.Contains(string(body), d.Path+" ("+d.Mode+")") {
					t.Errorf("system prompt lacks %s (%s)", d.Path, d.Mode)
				}
			}
		})
	}
}

func TestAdditionalDirsInvalidExit2(t *testing.T) {
	dir, emptyCfg, ref, _ := additionalDirsSetup(t)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	cfg := func(name, data string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(data), 0o644)
		return p
	}
	for _, c := range []struct {
		name, want string
		args       []string
	}{
		{"missing", "does not exist", []string{"-c", emptyCfg, "--add-dir-read", filepath.Join(ref, "nope")}},
		{"inside cwd", "inside the working directory", []string{"-c", emptyCfg, "--add-dir-write", sub}},
		{"contains cwd", "contains the working directory", []string{"-c", emptyCfg, "--add-dir-read", filepath.Dir(dir)}},
		{"filesystem root", "filesystem root", []string{"-c", emptyCfg, "--add-dir-read", "/"}},
		{"config without mode", "mode is required", []string{"-c", cfg("nomode.yaml", "additional_dirs: [{path: "+ref+"}]\n")}},
		{"config relative", "absolute path", []string{"-c", cfg("rel.yaml", "additional_dirs: [{path: ref, mode: read}]\n")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResponses{replies: []string{responsesSubmit}}
			t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
			var code int
			var errOut string
			stderr := captureStderr(t, func() {
				code, _, errOut = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--openai-api", "responses", "--no-stream"}, c.args...)...)
			})
			stderr += errOut
			if code != 2 || !strings.Contains(stderr, c.want) || len(f.bodies) != 0 {
				t.Errorf("exit %d, %d requests, stderr:\n%s", code, len(f.bodies), stderr)
			}
		})
	}
}

func TestAdditionalDirsRunStarted(t *testing.T) {
	dir, emptyCfg, ref, _ := additionalDirsSetup(t)
	f := &fakeResponses{replies: []string{responsesSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	code, stdout, _ := runCLI(t, "-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--openai-api", "responses", "--no-stream", "--json", "-c", emptyCfg, "--add-dir-read", ref)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.Contains(line, `"run.started"`) {
			continue
		}
		if !strings.Contains(line, `"additional_dirs":[{"path":"`+ref+`","mode":"read"}]`) {
			t.Errorf("run.started lacks additional_dirs: %s", line)
		}
		return
	}
	t.Errorf("no run.started event in:\n%s", stdout)
}

func TestAdditionalDirsSandboxWritable(t *testing.T) {
	_, _, ref, out := additionalDirsSetup(t)
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Sandbox = true
	env, closeEnv, err := startEnvironment(context.Background(), cfg, root, []tools.ExtraDir{{Path: ref, Mode: tools.DirRead}, {Path: out, Mode: tools.DirWrite}}, false)
	if err != nil {
		t.Skipf("sandbox unavailable on this host: %v", err)
	}
	defer closeEnv()
	sb := env.(tools.Local).Sandbox
	if !slices.Contains(sb.Writable, out) || slices.Contains(sb.Writable, ref) {
		t.Errorf("sandbox writable = %v; want %s and not %s", sb.Writable, out, ref)
	}
}

func TestResumeIgnoresAdditionalDirsNotice(t *testing.T) {
	tr, _, _, _ := resumeFixture(t)
	dirs := []tools.ExtraDir{{Path: "/srv/ref", Mode: tools.DirRead}}
	tr.SystemPrompt += tools.ExtraDirsNotice(dirs)
	tr.Messages[0].Content = tr.SystemPrompt
	tr.AdditionalDirs = agent.AdditionalDirsInfo(dirs, false)
	if err := agent.ValidateResume(tr, config.Default(), tr.Root); err != nil {
		t.Errorf("resume with recorded additional dirs: %v", err)
	}
}
