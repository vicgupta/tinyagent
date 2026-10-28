package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/config"
)

const starterConfig = `# tinyagent user config. Flags override these values. Full reference: README "Config".
# model: openai/gpt-4o        # same <provider>/<model> syntax as -m
# step_limit: 50
# cost_limit: 2.0
# wall_time_seconds: 1800
# read_only: false
# redact_secrets:             # mask known secret values in command, MCP, web_fetch and hook output
#   enabled: false            # same as --redact-secrets
# ask_user:                   # let the model end a run with one question (exit 4)
#   enabled: false            # answer with: --resume run.json -t "<answer>"
#   max_questions: 3          # per run, across resumes (1-20)
# memory:                     # project memory: notes runs save for later runs (same as --memory)
#   enabled: false            # load active entries and offer the remember tool
#   approval: pending         # pending (approve with --memory-approve) or auto
#   max_writes_per_run: 5
#   max_entries: 50
#   max_bytes: 8192           # cap on the Project memory prompt section (1-65536)
# untrusted_content:          # lockdown: once a web_fetch or MCP result or a review diff enters the run,
#   enabled: false            # refuse commands, MCP tools and other web_fetch hosts (same as --lockdown)
#   lockdown:                 # sources and per-MCP-server untrusted/lockdown_allowed: README "Untrusted-content lockdown"
#     web_fetch_allowed_domains: []  # web_fetch hosts still readable after taint
#     allowed_domains: []            # hosts --sandbox commands still reach after taint (also in sandbox_allowed_domains)
#     container_proxy: false         # container environment: reach allowed_domains through a proxy after taint
# attempts:                   # best-of-N, used only with --attempts N (README "Best-of-N attempts")
#   checks:                   # each must exit 0 in an attempt's worktree; --attempt-check replaces the list
#     - command: go test ./...
#       timeout_seconds: 600  # default 600, up to 3600
#   stop_on_pass: true        # false runs all N attempts and compares them
#   keep: all                 # non-winning worktrees to keep once one wins: all, failed or none
`

func runInitConfig(path string) int {
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: config path:", err)
			return 2
		}
	}
	if err := writeStarterConfig(path); err != nil {
		fmt.Fprintf(os.Stderr, "error: init config %s: %v\n", path, err)
		return 2
	}
	fmt.Println(path)
	return 0
}

func writeStarterConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Exclusive creation also refuses symlinks and avoids an overwrite race.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(starterConfig)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
