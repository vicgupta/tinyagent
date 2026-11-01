package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/config"
)

const starterConfig = `# tinyagent project config (./tinyagent.yaml). Flags override these values. Full reference: docs/config.md.
## Keep secrets in env vars (api_key_env names them); this file may be committed.
## mcp_servers, hooks, command_tools, lsp.servers, plugins (and other executables) need --trust-config here.
# model: openai/gpt-4o        # same <provider>/<model> syntax as -m
# step_limit: 50
# cost_limit: 2.0
# token_limit: 0              # max input+output tokens, delegates and workers included (0 = unlimited)
# wall_time_seconds: 1800
# read_only: false
# redact_secrets:             # mask known secret values in command, MCP, web_fetch and hook output
#   enabled: false            # same as --redact-secrets
# ask_user:                   # let the model end a run with one question (exit 4)
#   enabled: false            # answer with: --resume run.json -t "<answer>"
#   max_questions: 3          # per run, across resumes (1-20)
# memory:                     # project memory: notes runs save for later runs (same as --memory)
#   enabled: false            # load active entries; offer remember, memory_search, memory_list
#   approval: pending         # pending (approve with --memory-approve) or auto
#   max_writes_per_run: 5
#   max_entries: 50
#   max_bytes: 8192           # cap on the Project memory prompt section (1-65536)
#   backend: markdown         # markdown (./.tinyagent/memory/<id>.md) or sqlite (./.tinyagent/memory/memory.sqlite)
#   path: ./.tinyagent/memory # markdown folder, relative to --cwd and inside it
#   sqlite_path: ./.tinyagent/memory/memory.sqlite
#   max_entry_size: 500       # characters per entry (1-4000)
#   default_tags: []          # tags added to every new entry
#   # memory.toml and TINYAGENT_MEMORY_* override these: tinyagent --memory-help
# untrusted_content:          # lockdown: once a web_fetch or MCP result or a review diff enters the run,
#   enabled: false            # refuse commands, MCP tools and other web_fetch hosts (same as --lockdown)
#   lockdown:                 # sources and per-MCP-server untrusted/lockdown_allowed: docs/permissions.md "Untrusted-content lockdown"
#     web_fetch_allowed_domains: []  # web_fetch hosts still readable after taint
#     allowed_domains: []            # hosts --sandbox commands still reach after taint (also in sandbox_allowed_domains)
#     container_proxy: false         # container environment: reach allowed_domains through a proxy after taint
# attempts:                   # best-of-N, used only with --attempts N (docs/runs.md "Best-of-N attempts")
#   checks:                   # each must exit 0 in an attempt's worktree; --attempt-check replaces the list
#     - command: go test ./...
#       timeout_seconds: 600  # default 600, up to 3600
#   stop_on_pass: true        # false runs all N attempts and compares them
#   keep: all                 # non-winning worktrees to keep once one wins: all, failed or none
# plugins: []                # Agent Plugins packages (docs/plugins.md), e.g. - path: /abs/plugin-dir
#                             # (holds plugin.json); its skills join with --skills, and only mcp.json servers
#                             # listed under its mcp_servers start; inspect with --list-plugins
`

// runInitConfig handles tinyagent init (--init-config): it writes the
// starter config, or with --init-migrate a copy of the old per-user config,
// to -c PATH or ./tinyagent.yaml in --cwd. It refuses to overwrite unless
// --init-force, and always refuses a symlink.
func runInitConfig(o *options) int {
	path := o.configPath
	if path == "" {
		path = filepath.Join(o.cwd, config.ProjectFile)
	}
	content := starterConfig
	if o.initMigrate {
		old, err := config.DefaultPath()
		if err == nil {
			var b []byte
			if b, err = os.ReadFile(old); err == nil {
				content = string(b)
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: init --migrate: no user config to copy: %v\n", err)
			return 2
		}
		if abs, _ := filepath.Abs(path); abs == old {
			fmt.Fprintf(os.Stderr, "error: init --migrate: %s is the user config itself\n", path)
			return 2
		}
	}
	if err := writeConfigFile(path, content, o.configPath != "", o.initForce); err != nil {
		fmt.Fprintf(os.Stderr, "error: init config %s: %v\n", path, err)
		if errors.Is(err, fs.ErrExist) && !o.initForce {
			fmt.Fprintln(os.Stderr, "hint: pass --force (tinyagent init --force) to replace it")
		}
		return 2
	}
	fmt.Println(path)
	if o.configPath == "" {
		fmt.Fprintf(os.Stderr, "hint: keep secrets in env vars (api_key_env, env: names), never in %s; it may be committed with the project\n", config.ProjectFile)
	}
	return 0
}

// writeConfigFile creates path with mode 0600. mkdir creates missing parent
// directories (for -c). Without force it uses exclusive creation, which also
// refuses symlinks and avoids an overwrite race; with force it refuses a
// symlink and replaces the file atomically through a temp file and rename.
func writeConfigFile(path, content string, mkdir, force bool) error {
	if mkdir {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
	}
	if !force {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(content)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing to replace a symlink")
		}
		if !fi.Mode().IsRegular() {
			return errors.New("not a regular file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tinyagent-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.WriteString(content)
	if err := tmp.Chmod(0600); err != nil && writeErr == nil {
		writeErr = err
	}
	if err := tmp.Close(); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return writeErr
	}
	return os.Rename(tmp.Name(), path)
}
