package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func resumeFixture(t *testing.T) (*trajectory.Trajectory, string, string, string) {
	t.Helper()
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfg, []byte("model: codex/ignored-config-default\n"), 0600); err != nil {
		t.Fatal(err)
	}
	system := agent.SystemPrompt(config.Default(), root, false)
	tr := &trajectory.Trajectory{
		Task: "saved task", Model: "openai/gpt-4o", SchemaVersion: trajectory.SchemaVersion, Root: root,
		SystemPrompt: system, ToolSignature: agent.ToolSignature(), ExitStatus: agent.StatusError, Submission: "previous provider failure",
		Steps: 1, Cost: 0.01, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 9, CacheWriteTokens: 7,
		Messages: []model.Message{
			{Role: "system", Content: system}, {Role: "user", Content: "saved task"},
			{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "historic", Type: "function", Function: model.FunctionCall{Name: "write_file", Arguments: `{"path":"must-not-replay","content":"bad"}`}}}},
			{Role: "tool", ToolCallID: "historic", Content: "ok"},
		},
	}
	for _, spec := range tools.Specs() {
		tr.Tools = append(tr.Tools, spec.Function.Name)
	}
	source := filepath.Join(root, "source.json")
	if err := tr.Save(source); err != nil {
		t.Fatal(err)
	}
	return tr, source, filepath.Join(root, "continued.json"), cfg
}

func TestCLIResumePersistsBeforeQueryAndPreservesSource(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	if err := os.WriteFile(filepath.Join(tr.Root, "AGENTS.md"), []byte("Keep this project instruction."), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.SystemPrompt = agent.SystemPrompt(config.Default(), tr.Root, false)
	tr.Messages[0].Content = tr.SystemPrompt
	if err := tr.Save(source); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(source)
	t.Setenv("AGENT_MODEL", "codex/ignored-env-default")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		starting, err := trajectory.Load(output)
		if err != nil {
			t.Errorf("starting checkpoint not readable before query: %v", err)
		} else if starting.ExitStatus != "" || starting.Submission != "" || starting.Resume == nil || starting.Resume.ExitStatus != agent.StatusError || starting.Resume.Submission != "previous provider failure" {
			t.Error("starting state/provenance incorrect")
		}
		var body struct {
			Model    string
			Messages []model.Message
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "gpt-4o" || len(body.Messages) != 5 || body.Messages[4].Content != trajectory.ContinuationNotice {
			t.Error("saved model/history not restored")
		}
		if strings.Count(body.Messages[0].Content, "Keep this project instruction.") != 1 {
			t.Error("AGENTS.md instructions missing or duplicated")
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`))
	}))
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--no-stream", "--step-limit", "40")
	})
	if code != 0 || calls != 1 {
		t.Fatalf("exit %d, calls %d: %s", code, calls, stderr)
	}
	if !strings.Contains(stderr, "previous status Error after 1 steps") || !strings.Contains(stderr, "unrecorded side effects") || !strings.Contains(stderr, "200 input / 40 output tokens") {
		t.Fatalf("missing feedback: %s", stderr)
	}
	after, _ := os.ReadFile(source)
	if string(before) != string(after) {
		t.Fatal("source changed")
	}
	if _, err := os.Stat(filepath.Join(tr.Root, "must-not-replay")); !os.IsNotExist(err) {
		t.Fatal("replayed historical write")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got trajectory.Trajectory
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Steps != 2 || got.InputTokens != 200 || got.OutputTokens != 40 || got.Cost <= tr.Cost || got.CacheReadTokens != 9 || got.CacheWriteTokens != 7 {
		t.Fatalf("accounting: %+v", got)
	}
}

func TestCLIResumeRefusalsBeforeProviderSetup(t *testing.T) {
	for _, which := range []string{"task", "empty task", "model", "missing root", "wrong root", "instructions", "mode", "tool signature", "tools", "existing", "source", "symlink", "hardlink", "dangling symlink", "bad history", "legacy", "compacted"} {
		t.Run(which, func(t *testing.T) {
			tr, source, output, cfg := resumeFixture(t)
			// A Codex model would read local auth during setup. Every refusal must
			// happen before that, even if the provider cannot initialize.
			tr.Model = "codex/gpt-5"
			t.Setenv("CODEX_HOME", t.TempDir())
			args := []string{"--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--no-stream"}
			want := ""
			switch which {
			case "task":
				args = append(args, "--task", "other")
				want = "mutually exclusive"
			case "empty task":
				args = append(args, "-t", "")
				want = "mutually exclusive"
			case "model":
				args = append(args, "-m", "openai/different")
				want = "conflicts"
			case "missing root":
				args = append(args[:2], args[4:]...)
				want = "explicit --cwd"
			case "wrong root":
				args[3] = t.TempDir()
				want = "root"
			case "instructions":
				if err := os.WriteFile(filepath.Join(tr.Root, "AGENTS.md"), []byte("new instructions"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "prompt"
			case "mode":
				args = append(args, "--no-agents-md")
				want = "loading mode"
			case "tool signature":
				tr.ToolSignature = "other"
				want = "compatibility"
			case "tools":
				tr.Tools = tr.Tools[:1]
				want = "tool"
			case "existing":
				if err := os.WriteFile(output, []byte("preserve me"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "exists"
			case "source":
				args[7] = source
				want = "distinct"
			case "symlink":
				if err := os.Symlink(source, output); err != nil {
					t.Skip(err)
				}
				want = "distinct"
			case "hardlink":
				if err := os.Link(source, output); err != nil {
					t.Skip(err)
				}
				want = "distinct"
			case "dangling symlink":
				if err := os.Symlink(source+".missing", output); err != nil {
					t.Skip(err)
				}
				want = "exists"
			case "bad history":
				tr.Messages = tr.Messages[:3]
				want = "result"
			case "legacy":
				tr.SchemaVersion = 0
				want = "metadata"
			case "compacted":
				tr.Compactions = []trajectory.Compaction{{Step: 1}}
				want = "compacted"
			}
			if err := tr.Save(source); err != nil {
				t.Fatal(err)
			}
			if which == "hardlink" {
				if err := os.Remove(output); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(source, output); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(source)
			code := 0
			stderr := captureStderr(t, func() { code, _, _ = runWith(t, args...) })
			if code == 0 || !strings.Contains(stderr, want) || strings.Contains(stderr, "no login") {
				t.Fatalf("exit %d, want %s: %s", code, want, stderr)
			}
			after, _ := os.ReadFile(source)
			if string(before) != string(after) {
				t.Fatal("source modified on refusal")
			}
			if which == "existing" {
				b, _ := os.ReadFile(output)
				if string(b) != "preserve me" {
					t.Fatal("existing destination modified")
				}
			}
		})
	}
}

func TestCLIResumeExhaustedLimitCanResumeAgain(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	// Point at a local server to prove no request is made with exhausted totals.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	for _, which := range []string{"step", "cost"} {
		t.Run(which, func(t *testing.T) {
			dest := output + which
			limit := []string{"--step-limit", "1"}
			if which == "cost" {
				limit = []string{"--cost-limit", "0.01"}
			}
			args := append([]string{"--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", dest, "--no-stream"}, limit...)
			stderr := captureStderr(t, func() {
				code, _, _ := runWith(t, args...)
				if code != 1 {
					t.Errorf("exit %d", code)
				}
			})
			if calls != 0 {
				t.Fatal("request with exhausted ceiling")
			}
			if !strings.Contains(stderr, "after 1 steps in 0s") {
				t.Fatalf("elapsed/cumulative summary: %s", stderr)
			}
			c, err := trajectory.Load(dest)
			if err != nil || c.Resume == nil || c.Resume.ExitStatus != agent.StatusError {
				t.Fatalf("new interrupted boundary: %v", err)
			}
			next := dest + "-next"
			captureStderr(t, func() {
				code, _, _ := runWith(t, "--resume", dest, "--cwd", tr.Root, "-c", cfg, "-o", next, "--no-stream", "--step-limit", "2", "--cost-limit", "0")
				if code != 0 {
					t.Errorf("second resume exit %d", code)
				}
			})
			if calls != 1 {
				t.Fatalf("queries after increased limit: %d", calls)
			}
			calls = 0
		})
	}
}

func TestCLIResumeCanonicalRootAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	tr, source, output, cfg := resumeFixture(t)
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(tr.Root, alias); err != nil {
		t.Skip(err)
	}
	stderr := captureStderr(t, func() {
		code, _, _ := runWith(t, "--resume", source, "--cwd", alias, "-c", cfg, "-o", output, "--step-limit", "1")
		if code != 1 {
			t.Errorf("exit %d", code)
		}
	})
	if strings.Contains(stderr, "does not match") {
		t.Fatal(stderr)
	}
	if _, err := trajectory.Load(output); err != nil {
		t.Fatal(err)
	}
}

func TestCLIResumeInitialSaveFailure(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	blocker := filepath.Join(tr.Root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	output = filepath.Join(blocker, "out.json")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{}`)) }))
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	stderr := captureStderr(t, func() {
		code, _, _ := runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output)
		if code != 1 {
			t.Errorf("exit %d", code)
		}
	})
	if calls != 0 || !strings.Contains(stderr, "error:") && !strings.Contains(stderr, "saving trajectory") {
		t.Fatalf("calls %d: %s", calls, stderr)
	}
	if _, err := trajectory.Load(source); err != nil {
		t.Fatal(err)
	}
}

func TestCLIResumeRetainsRecordedSkipAgentsMode(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	tr.SkipAgentsMD = true
	if err := tr.Save(source); err != nil {
		t.Fatal(err)
	}
	// The saved invocation excluded AGENTS.md. Its current contents must not
	// silently change that mode when the flag is absent on the next invocation.
	if err := os.WriteFile(filepath.Join(tr.Root, "AGENTS.md"), []byte("new instructions excluded by recorded mode"), 0600); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		code, _, _ := runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--step-limit", "1")
		if code != 1 {
			t.Errorf("exit %d", code)
		}
	})
	loaded, err := trajectory.Load(output)
	if err != nil {
		t.Fatalf("recorded loading mode not retained: %v; %s", err, stderr)
	}
	if !loaded.SkipAgentsMD || loaded.Messages[0].Content != tr.SystemPrompt {
		t.Fatal("saved instructions changed")
	}
}
