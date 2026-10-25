package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

const chatDelegate = `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"p1","type":"function","function":{"name":"delegate","arguments":"{\"task\":\"list the files\",\"label\":\"survey\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`
const chatReport = `{"choices":[{"message":{"role":"assistant","content":"There are no source files."},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`

func TestSubagentsFlagAndConfig(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	f := &fakeGateway{replies: []string{chatSubmit}}
	srv := f.start(t)
	base := "providers:\n  - id: local\n    api: chat\n    base_url: " + srv.URL + "\n"
	off := providerCfg(t, dir, base)
	on := providerCfg(t, dir, base+"subagents:\n  enabled: true\n  max_steps: 5\n")
	for _, c := range []struct {
		name string
		want bool
		args []string
	}{
		{"default", false, []string{"-c", off}},
		{"flag", true, []string{"-c", off, "--subagents"}},
		{"config", true, []string{"-c", on}},
		{"flag beats config", false, []string{"-c", on, "--subagents=false"}},
		{"read-only", true, []string{"-c", off, "--subagents", "--read-only"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := f.count()
			code, _, stderr := runCLI(t, append([]string{"-t", "x", "-m", "local/m1", "--cwd", dir}, c.args...)...)
			if code != 0 || f.count() != before+1 {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			names := sentToolNames(f.bodies[before])
			if got := slices.Contains(names, "delegate"); got != c.want {
				t.Errorf("delegate sent = %v, want %v (tools %v)", got, c.want, names)
			}
		})
	}
}

// TestSubagentsRunObservability runs a delegate through the CLI and checks
// --json, -o and --log-db.
func TestSubagentsRunObservability(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	f := &fakeGateway{replies: []string{chatDelegate, chatReport, chatSubmit}}
	srv := f.start(t)
	db := filepath.Join(t.TempDir(), "log.db")
	cfg := providerCfg(t, dir, "providers:\n  - id: local\n    api: chat\n    base_url: "+srv.URL+"\nlog_db:\n  enabled: true\n  path: "+db+"\n")
	traj := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "local/m1", "--cwd", dir, "-c", cfg, "--subagents", "--json", "-o", traj)
	if code != 0 || f.count() != 3 {
		t.Fatalf("exit %d, %d requests; stderr:\n%s", code, f.count(), stderr)
	}
	if got := sentToolNames(f.bodies[1]); !slices.Equal(got, []string{"read_file", "list_files", "grep"}) {
		t.Errorf("child tools = %v", got)
	}
	var started, finished, childEvent bool
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev["type"] {
		case "subagent.started":
			started = ev["id"] == "d1" && ev["label"] == "survey" && ev["model"] == "local/m1"
		case "subagent.finished":
			finished = ev["id"] == "d1" && ev["status"] == "Completed"
		case "assistant.message":
			if ev["subagent"] == "d1" {
				childEvent = true
			}
		case "run.finished":
			if ev["subagent_steps"] != float64(1) {
				t.Errorf("run.finished = %v", ev)
			}
		}
	}
	if !started || !finished || !childEvent {
		t.Errorf("events started=%v finished=%v child=%v:\n%s", started, finished, childEvent, stdout)
	}
	tr, err := trajectory.Load(traj)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.SubagentsEnabled || len(tr.Subagents) != 1 || tr.Subagents[0].Report != "There are no source files." || len(tr.Subagents[0].Messages) != 3 || tr.SubagentSteps != 1 || tr.Steps != 2 {
		t.Errorf("trajectory subagents = %+v (steps %d)", tr.Subagents, tr.Steps)
	}
	if s := readDB(t, db); !strings.Contains(s, "subagent.started") || !strings.Contains(s, "subagent.finished") {
		t.Error("log-db lacks subagent events")
	}
}

func TestSubagentsModelValidation(t *testing.T) {
	dir, _ := openaiAPISetup(t)
	f := &fakeGateway{replies: []string{chatSubmit}}
	srv := f.start(t)
	base := "providers:\n  - id: local\n    api: chat\n    base_url: " + srv.URL + "\n"
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Model problems exit 2 like -m; config load errors keep their exit 1.
	for _, c := range []struct {
		name, cfg, want string
		code            int
	}{
		{"unknown provider", "subagents:\n  enabled: true\n  model: nope/x\n", `subagents.model "nope/x"`, 2},
		{"missing key", "subagents:\n  enabled: true\n  model: anthropic/claude-sonnet-4-5\n", "ANTHROPIC_API_KEY is not set", 2},
		{"unknown key", "subagents:\n  enabled: true\n  modle: x\n", "field modle not found", 1},
		{"negative max_steps", "subagents:\n  max_steps: -1\n", "subagents.max_steps", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, "-t", "x", "-m", "local/m1", "--cwd", dir, "-c", providerCfg(t, dir, base+c.cfg))
			if code != c.code || !strings.Contains(stderr, c.want) || f.count() != 0 {
				t.Errorf("exit %d, %d requests; stderr:\n%s", code, f.count(), stderr)
			}
		})
	}
	// Off, a bad child model is never resolved.
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "local/m1", "--cwd", dir, "-c", providerCfg(t, dir, base+"subagents:\n  model: nope/x\n")); code != 0 {
		t.Errorf("disabled subagents: exit %d: %s", code, stderr)
	}
}

func TestResumeSubagentsSettingMustMatch(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--subagents")
	})
	if code != 2 || !strings.Contains(stderr, "--subagents conflicts with the checkpoint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestSameProvider(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"openai/gpt-5", "gpt-4o", true},
		{"anthropic/x", "anthropic/y", true},
		{"anthropic/x", "openai/y", false},
	} {
		if got := sameProvider(c.a, c.b); got != c.want {
			t.Errorf("sameProvider(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
