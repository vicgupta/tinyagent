package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestCLIResumeRejectsMCPCheckpoint pins that --resume refuses a checkpoint
// whose run used MCP or plugin tools (agent.New rejects the same
// combination): it fails before any provider request and writes no output
// checkpoint.
func TestCLIResumeRejectsMCPCheckpoint(t *testing.T) {
	for _, mark := range []string{"mcp", "external tools"} {
		t.Run(mark, func(t *testing.T) {
			tr, source, output, cfg := resumeFixture(t)
			if mark == "mcp" {
				tr.MCPEnabled = true
			} else {
				tr.ExternalTools = []string{"srv__tool"}
			}
			if err := tr.Save(source); err != nil {
				t.Fatal(err)
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
			defer srv.Close()
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			code := 0
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--no-stream")
			})
			if code != 1 || calls != 0 || !strings.Contains(stderr, "cannot be resumed; start a fresh task") {
				t.Fatalf("exit %d, requests %d, stderr %q", code, calls, stderr)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("output checkpoint written: %v", err)
			}
		})
	}
}

// TestACPOffersCommandTools pins the session-turn fix: ACP (and chat) turns
// built through agent.New get the configured command tools on every turn,
// like a headless run, and read-only sessions still don't.
func TestACPOffersCommandTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable is a shell script")
	}
	exe := filepath.Join(t.TempDir(), "fakego")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, "model: openai/gpt-4o\ncommand_tools:\n  - name: run_tests\n    description: Run Go tests.\n    argv: ["+exe+", test]\n")
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprint("read-only=", readOnly), func(t *testing.T) {
			root := acpWorkspace(t)
			p := newACPProvider(t)
			args := []string{"-c", cfg, "--no-stream"}
			if readOnly {
				args = append(args, "--read-only")
			}
			c := startACP(t, args...)
			sess := c.newSession(root)
			for _, msg := range []string{"first", "second"} {
				if stop, _, _ := c.prompt(sess, msg); stop != "end_turn" {
					t.Fatalf("%s: stop %q", msg, stop)
				}
			}
			reqs := p.requests()
			if len(reqs) != 2 {
				t.Fatalf("%d requests", len(reqs))
			}
			for i, r := range reqs {
				if got := slices.Contains(requestTools(r), "run_tests"); got == readOnly {
					t.Errorf("turn %d: run_tests offered = %v, tools %v", i+1, got, requestTools(r))
				}
			}
			c.close()
		})
	}
}
