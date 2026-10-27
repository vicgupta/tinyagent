package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

func TestACPWorkersCommitAndApplyAcrossTurns(t *testing.T) {
	for _, mode := range []string{"flag", "config"} {
		t.Run(mode, func(t *testing.T) {
			root := worktreeRepo(t)
			head := gitT(t, root, "rev-parse", "HEAD")
			p := newACPProvider(t,
				acpReply{tool: "worker", args: `{"task":"write worker.txt"}`},
				acpReply{tool: "write_file", args: `{"path":"worker.txt","content":"from worker\n"}`},
				acpReply{text: "implemented\n" + agent.SubmitMarker},
				acpReply{text: "review ready\n" + agent.SubmitMarker},
				acpReply{tool: "apply_worker", args: `{"worker":"w1"}`},
			)
			cfg := "model: openai/gpt-4o\n"
			args := []string{"--no-stream"}
			if mode == "config" {
				cfg += "subagents:\n  workers:\n    enabled: true\n"
			} else {
				args = append(args, "--workers")
			}
			args = append(args, "-c", writeConfig(t, cfg))
			c := startACP(t, args...)
			sess := c.newSession(root)
			stop, _, updates := c.prompt(sess, "implement in a worker")
			if stop != "end_turn" {
				t.Fatalf("stop = %q: %v", stop, updates)
			}
			reqs := p.requests()
			if !slices.Contains(requestTools(reqs[0]), "worker") || !slices.Contains(requestTools(reqs[0]), "apply_worker") {
				t.Fatalf("worker tools missing: %v", requestTools(reqs[0]))
			}
			if _, err := os.Stat(filepath.Join(root, "worker.txt")); !os.IsNotExist(err) {
				t.Fatalf("worker changed parent: %v", err)
			}
			if got := gitT(t, root, "rev-parse", "HEAD"); got != head {
				t.Fatalf("HEAD changed: %s", got)
			}
			branches := gitT(t, root, "for-each-ref", "--format=%(refname:short)", "refs/heads/tinyagent/")
			if branches == "" {
				t.Fatal("no worker branch")
			}
			if got := gitT(t, root, "show", branches+":worker.txt"); got != "from worker" {
				t.Fatalf("worker commit: %q", got)
			}
			if stop, r, _ := c.prompt(sess, "apply w1"); stop != "end_turn" {
				t.Fatalf("follow-up: %v", r)
			}
			b, err := os.ReadFile(filepath.Join(root, "worker.txt"))
			if err != nil || string(b) != "from worker\n" {
				t.Fatalf("apply: %q %v", b, err)
			}
			c.close()
		})
	}
}

func TestACPWorkersEnablementAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, cfg, want string
		args            []string
		repo            bool
		empty           bool
	}{
		{name: "default off"},
		{name: "flag overrides config", cfg: "subagents:\n  workers:\n    enabled: true\n", args: []string{"--workers=false"}},
		{name: "read only", args: []string{"--workers", "--read-only"}, repo: true, want: "--read-only"},
		{name: "not git", args: []string{"--workers"}, want: "needs a Git repository"},
		{name: "no commits", args: []string{"--workers"}, empty: true, want: "needs a commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.repo {
				root = worktreeRepo(t)
			}
			if tc.empty {
				gitT(t, root, "init", "-q")
			}
			p := newACPProvider(t)
			args := append([]string{"-c", writeConfig(t, "model: openai/gpt-4o\n"+tc.cfg), "--no-stream"}, tc.args...)
			c := startACP(t, args...)
			r, _ := c.wait(c.call("session/new", map[string]any{"cwd": root, "mcpServers": []any{}}))
			if tc.want != "" {
				if r["error"] == nil || !strings.Contains(strings.ToLower(r["error"].(map[string]any)["message"].(string)), strings.ToLower(tc.want)) {
					t.Errorf("session/new: %v; want %q", r, tc.want)
				}
				if len(p.requests()) != 0 {
					t.Error("validation made model calls")
				}
			} else {
				res, ok := r["result"].(map[string]any)
				if !ok {
					t.Fatalf("session/new: %v", r)
				}
				c.prompt(res["sessionId"].(string), "hello")
				ts := requestTools(p.requests()[0])
				if slices.Contains(ts, "worker") || slices.Contains(ts, "apply_worker") {
					t.Errorf("workers enabled: %v", ts)
				}
			}
			c.close()
		})
	}
}
