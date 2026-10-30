# Sandboxing and execution environments

## Sandboxing commands

`--sandbox` (or `sandbox: true`) confines every `bash`, `start_process` and [command tool](config.md#command-tools) command, and all processes they spawn, at the OS level. Commands can write only inside `--cwd`, a per-run temporary directory (exported as `TMPDIR`, removed when the run ends), the directories in `sandbox_writable_paths` and any `write` [additional directories](permissions.md#additional-directories). Reads stay unrestricted so toolchains work. Outbound network is denied unless you pass `--sandbox-network` (`sandbox_network: true`). Flags override the config, including `--sandbox=false`. It is off by default.

```yaml
sandbox: true
sandbox_network: false
sandbox_writable_paths:        # absolute paths to existing directories
  - /home/me/.cache/go-build
```

Toolchains that cache outside the repo (Go, npm, pip) fail under the sandbox with `Permission denied` until their cache directories are listed in `sandbox_writable_paths`.

- **Linux:** Landlock, applied by a re-executed helper to the command only (tinyagent itself is unrestricted). Needs Linux 6.2+ (Landlock ABI 3) to confine writes, and 6.7+ (ABI 4) to deny TCP. Landlock cannot restrict UDP before ABI 10 and does not cover Unix sockets, so "network denied" means TCP connect and bind. WSL2 counts as Linux.
- **macOS:** Seatbelt through `/usr/bin/sandbox-exec` (deprecated by Apple but still shipped). This backend has not been tested in CI.
- **Windows:** not supported.
- **Fail closed:** if the host cannot enforce the requested policy, tinyagent exits before the first model request and says what is missing. It never runs commands unsandboxed.
- On start, one `sandbox:` line (backend, writable roots, network) goes to stderr, and `-o` trajectories record it as `sandbox`. With `--resume`, the current invocation's settings apply. With `--read-only` there are no command tools, so nothing is sandboxed.
- **Not covered:** [`web_fetch`](long-runs.md#web-fetch), which has its own allowlist and is unaffected by `--sandbox`, the in-process file tools (`read_file`, `write_file`, ... and their symlink behavior; see [Restricting file access](permissions.md#restricting-file-access) for rules on them), MCP servers, and provider requests. Commands can still read anything you can, including credentials, and inherit your environment variables (API keys included) unless you set a [command environment](#command-environment) policy. With `--sandbox-network` they can send it to any host; prefer a [network allowlist](#network-allowlist). The sandbox reduces risk; it does not make untrusted code safe.

### Network allowlist

`sandbox_allowed_domains` (or `--sandbox-allow-domain <host>`, repeatable, which replaces the config list) lets sandboxed commands reach only the hosts you list, over HTTP and HTTPS. tinyagent runs a small proxy outside the sandbox that checks each request's hostname, and sets `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` (and their lowercase forms) to it, with `NO_PROXY` empty. The OS sandbox lets commands connect only to that proxy. It requires `--sandbox` and can't be combined with `--sandbox-network` (exit 2 otherwise). Without the key or flag, nothing changes.

```yaml
sandbox: true
sandbox_allowed_domains:
  - proxy.golang.org       # Go modules
  - sum.golang.org
  - registry.npmjs.org     # npm
  - pypi.org               # pip
  - files.pythonhosted.org
sandbox_writable_paths:    # their caches, see above
  - /home/me/go/pkg/mod
```

- **Entries:** an exact hostname (`api.example.com`), `*.example.com` for its subdomains only (not `example.com` itself), or an IP literal (`10.0.0.5`, `[::1]`). Each may end with `:port`; otherwise ports 80 and 443 are allowed. Matching ignores case and a trailing dot. A bare `*` is a config error.
- **Private addresses:** the proxy resolves an allowed name itself and refuses to connect to loopback, private, link-local (including the `169.254.169.254` cloud metadata address) and other non-public addresses, unless that address is listed as a literal for the port.
- **Refusals:** the command gets HTTP `403` with an `X-Proxy-Error: blocked-by-allowlist` header and a body such as `tinyagent sandbox: host not in sandbox_allowed_domains: evil.example (port 443)` (for HTTPS, clients report the proxy's 403 on `CONNECT`). Each refusal is a `sandbox.network_denied` event with `--json`, is listed under `sandbox_network_denials` in `-o` trajectories, and is printed to stderr with `-v`. Allowed requests are not logged.
- **Only proxy-aware clients work:** curl, Go, npm, pip, git over HTTPS and most HTTP libraries honor the proxy variables. Programs that ignore them, and anything that isn't HTTP(S) (SSH, raw TCP, UDP, DNS lookups), cannot connect. Proxy variables in your own environment are replaced, and the proxy connects directly rather than through an upstream proxy.
- **Linux:** each command runs in its own unprivileged user and network namespace, which has only loopback. tinyagent forwards a loopback port there to the proxy's Unix socket (in a private temporary directory removed when the run ends), and Landlock additionally allows TCP connections only to that port. This needs Landlock ABI 4 and unprivileged user namespaces. Ubuntu 23.10+ restricts these with AppArmor (`kernel.apparmor_restrict_unprivileged_userns=1`), and other distributions may disable them (`kernel.unprivileged_userns_clone`, `user.max_user_namespaces`); there, tinyagent exits before the first model request with a message naming the cause. Commands appear as your own uid, but other users and groups map to `nobody`. Unix sockets on the filesystem (for example a Docker socket) are still reachable, as without the allowlist.
- **macOS:** the proxy listens on a loopback port and the Seatbelt profile allows outbound connections only to that port on localhost.
- **Exfiltration:** an allowlist limits where data can go, not what is sent. Commands can upload anything they can read to an allowed host, and broad entries (`github.com`, `*.amazonaws.com`, CDN wildcards) allow uploads to other users' content and domain fronting, since TLS isn't inspected. List only the hosts a task needs.
- The startup line shows e.g. `network: allowlist (2 domains: proxy.golang.org, sum.golang.org)`, and the trajectory's `sandbox` record includes `allowed_domains`. The proxy stops when the run ends, however it ends; background processes from `start_process` use it until then.


## Command environment

By default `bash`, `start_process` and [command tool](config.md#command-tools) commands inherit tinyagent's whole environment, including the provider API keys tinyagent itself needs, so a prompt-injected `env` or `echo $OPENAI_API_KEY` can read them. `command_env` (config only, from the user config or `-c`) controls what they get instead:

```yaml
command_env:
  inherit: core                       # all (default) | core | none
  exclude_secrets: true               # default false
  exclude: ["AWS_*", "NPM_TOKEN"]     # names or * ? [ ] globs
  pass: [GOFLAGS, GOPATH, GITHUB_TOKEN]   # names or globs copied from tinyagent's environment
  set:                                # literal string values
    CI: "true"
```

`--command-env all|core|none` overrides `inherit` for one run. The environment is built once per run, in this order:

1. **Base** from `inherit`: `all` is every variable; `core` keeps `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_*`, `TERM`, `TZ`, `TMPDIR`, plus on Windows `SystemRoot`, `WINDIR`, `USERPROFILE`, `TEMP`, `TMP`, `PATHEXT`, `COMSPEC`; `none` is empty.
2. **`exclude_secrets`** removes names ending in `_KEY`, `_TOKEN`, `_SECRET` or `_PASSWORD` (the same rule `--log-db` redaction uses) and always `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and `OPENROUTER_API_KEY`.
3. **`exclude`** removes matching names.
4. **`pass`** adds matching names back from tinyagent's environment, even ones removed above. Entries that match nothing are skipped (`-v` lists them).
5. **`set`** applies literal values.
6. With `--sandbox`, the sandbox's `TMPDIR` is applied last.

Name matching is case-insensitive on Windows and case-sensitive elsewhere. tinyagent's own environment is unchanged, so providers, MCP servers and hooks behave as before; every process a command spawns inherits the filtered environment, with or without `--sandbox`. An invalid `inherit`, an empty or `=`-containing name, a malformed glob, a non-string `set` value or an unknown key exits 2 before any provider request.

When a policy is set (or `--command-env` is passed), one line such as `command env: core, secrets excluded; 14 passed, 37 withheld` goes to stderr; `-v` also lists the passed and withheld names. `-o` trajectories and `--json` `run.started` record `command_env` (mode, `exclude_secrets`, passed names). Values are never printed or recorded; `--log-db` also redacts `set` values. With `--resume`, the current invocation's policy applies. `--read-only` runs have no command tools, so the policy has no effect there (it is still validated). The `container` environment forwards no host variables and ignores the policy.

For CI on untrusted input (issue bodies, PR diffs), use `inherit: core` with `exclude_secrets: true`; for reproducible runs, `inherit: none` with `set`. `core` and `none` can break toolchains that need other variables (`GOPATH`, `JAVA_HOME`, proxy settings): check the `-v` lists and add them to `pass`.

This is not a sandbox. It does not protect credentials stored in files (`~/.aws/credentials`, `.env`, the Codex auth file), which commands can still read ([`file_access`](permissions.md#restricting-file-access) rules hide them from the file tools only). It does not redact secrets from tool output by itself: `pass` and `set` deliberately give commands the values you name, so turn on [`redact_secrets`](permissions.md#redacting-secrets-from-tool-output) to keep those values out of what the model sees, trajectories and `--json`.

## Execution environments

The environment decides where `bash` and `start_process` commands run. Choose it with `--env local|container` or `environment:` in the config; the flag wins, and the default is `local`.

- **`local`** (default) runs `bash -c` on the host, exactly as before. `--sandbox` is a property of this environment (see [Sandboxing commands](#sandboxing-commands)).
- **`container`** runs each command with `<runtime> exec ... bash -c <command>` in one container started for the run:

```yaml
environment: container
container:
  runtime: docker          # or podman; default docker
  image: golang:1.23       # required; must provide bash
  run_args: [--network, none]   # optional extra `run` arguments, added after tinyagent's defaults
```

Before the first model request tinyagent runs `<runtime> run -d --rm --init --label tinyagent=1 -v <cwd>:<cwd> -w <cwd> --user <uid>:<gid> -e HOME=/tmp <run_args> <image> sleep infinity` and prints one line to stderr, e.g. `environment: container (docker, image=golang:1.23); mount: /home/me/repo`. `-o` trajectories record it as `environment` (name, runtime, image).

- **Mount model:** only `--cwd` (plus any [additional directories](permissions.md#additional-directories), read ones `:ro`) is shared, at the same path, so the in-process file tools on the host and commands in the container see the same files. Commands run as your uid:gid so files they create are yours on the host (for rootless Podman add `run_args: [--userns=keep-id]`). `$HOME` is `/tmp` inside the container; writes outside `--cwd` stay in the container. Later `run_args` such as `--user` or `-e HOME=...` override these defaults.
- **What runs where:** only `bash` and `start_process` commands run in the container. File tools, MCP servers, provider requests and the trajectory stay on the host. No host environment variables are forwarded (pass them with `run_args: [-e, NAME]` if needed). The network is the runtime's default unless `run_args` say otherwise.
- **Timeouts and `stop_process`** kill the command's processes inside the container (each command is tagged with a `TINYAGENT_EXEC` variable that its children inherit), not only the host-side `exec` client. Output format, return codes, the process limit and buffers are unchanged.
- **Cleanup:** the container is removed when the run ends: normally, on error, at a limit, or on Ctrl-C. `docker ps -a --filter label=tinyagent` lists any left behind by a killed tinyagent.
- **Fail closed:** a missing runtime, an unreachable daemon or an image that won't start exits with an error before any provider request. tinyagent never falls back to running commands on the host.
- `--sandbox` can't be combined with `--env container` (exit 2); use `run_args` such as `--network none` or `--read-only` to restrict the container. `--read-only` runs start no container. With `--resume`, the current invocation's environment applies. Not supported on Windows.
