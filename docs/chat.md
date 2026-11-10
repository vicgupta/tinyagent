# Interactive chat

`tinyagent chat` (or `tinyagent -i`) opens a conversation on the terminal: type a message, press Enter, read the reply, type the next one. Every message continues the same conversation, with the same tools, permissions, sandbox and config as a normal run.

```sh
tinyagent chat                          # start a session in the current directory
tinyagent chat -t "explain main.go"     # send a first message, then keep chatting
tinyagent chat -- explain main.go       # the same, without quoting
tinyagent chat --resume .tinyagent/chats/20261031-115411.json --cwd .   # pick a session back up
tinyagent goal -i                      # chat with plan-first and verify-on-stop; see [Goal mode](tasks.md#goal-mode)
```

Chat reads the model, API keys and other settings exactly as a normal run does: `-m`, then an exported `$AGENT_MODEL`, then `model:` in [`.tinyagent/tinyagent.yaml`](config.md#config) in `--cwd`, the first config file it reads. API keys come from exported environment variables, or from the config's [`env:`](config.md#environment-variables) map (`OPENAI_API_KEY: "sk-..."` in `.tinyagent/tinyagent.yaml`), applied before the missing-key check; a variable you exported wins.

To chat in a browser instead, run `tinyagent web`: the same session, tools and settings in a local web UI. See [Web UI](web.md).

Press Ctrl-C to cancel a running turn (if any) and exit (status 130), or Ctrl-D or `/exit` to quit (status 0). tinyagent prints the session's turns and cost, and the command that resumes it. An interrupted session is saved and can be resumed.

## What it looks like

Replies are Markdown, rendered for the terminal as each block completes: headings, **bold**, *italic*, `code`, fenced code in a box with its language and light highlighting, nested and task lists, blockquotes, tables, rules, and links with their URL. Text wraps to the terminal width (up to 120 columns). Tool calls collapse into one line per step; a spinner shows while the model thinks.

```text
tinyagent v0.15.0 · interactive chat
  model  openai/gpt-4o
  cwd    ~/src/app
  saves  .tinyagent/chats/20261031-115411.json
Type a message and press Enter. /help lists commands, Ctrl-C exits, Ctrl-D quits.

› what does main.go do?
Let me look.
  ✓ read_file main.go

Overview
━━━━━━━━

It parses flags and starts the server on port 8080.

• parse reads --addr and --debug
• serve calls http.ListenAndServe

╭─ go
│ func main() {
│     flag.Parse()
│     log.Fatal(http.ListenAndServe(*addr, nil))
│ }
╰─

turn 1 · 2 steps · 1,204 in / 96 out tokens · $0.0040 (session $0.0040) · 3s

› ^D
chat ended · 1 turn · 2 steps · 1,204 in / 96 out tokens · $0.0040 · 5s
  saved  .tinyagent/chats/20261031-115411.json
  resume tinyagent chat --resume /home/me/src/app/.tinyagent/chats/20261031-115411.json --cwd /home/me/src/app
```

Without color (`--color never`, `NO_COLOR`, or output that isn't a terminal) the structure stays and no escape codes are written: headings keep their `#`, bullets are `-`, code blocks use `+--` and `|`, and the tool line starts with `#`. `/raw`, or config `chat_markdown: false`, shows replies as raw Markdown. Saved checkpoints and `--json` always hold the raw Markdown.

## Live tool output

`bash` and configured `command_tools` display output on stderr while running, without requiring `stream: true`; `read_process` previews output from its wait too. The spinner clears before each chunk and does not overwrite partial lines. Terminal controls are removed from previews. `--no-stream` disables them, and explicit bash `stream: false` silences one call. Previews use configured secret redaction (complete-line buffering), are capped at 256 KiB per call, and are never saved in checkpoints, event logs or traces. Final tool results remain authoritative.

A [CLI handoff](models.md#cli-handoff-providers) provider (`claude-code`, `codex-exec`, `opencode`) streams the CLI's assistant text into the reply as it is produced, and shows the CLI's own tool and command activity with the same preview (the spinner names the CLI tool). The saved turn is still only the CLI's final answer, which is not printed again when it was already streamed. `--no-stream` waits for that answer.

## Keys

| Key | Action |
|---|---|
| Enter | Send the message |
| Paste | Multi-line clipboard text arrives as one message, where the terminal supports bracketed paste. Up to 8 MiB is kept intact; a larger paste is truncated with a warning |
| `\` at the end of a line | Continue the message on the next line |
| `"""` | Start or end a multi-line block |
| Up / Down (Ctrl-P / Ctrl-N) | Previous / next message from history |
| Left / Right, Home / End, Ctrl-A / Ctrl-E | Move the cursor |
| Ctrl-U / Ctrl-K / Ctrl-W | Delete to the start / to the end / the previous word |
| Ctrl-C while the model works | Cancel that turn (the model stream, tools and child processes) and exit (status 130); the session is saved and can be resumed |
| Ctrl-C at the prompt | Exit (status 130) and restore the terminal |
| Ctrl-D at an empty prompt | Quit (status 0) |

Line editing works on Linux and macOS. Elsewhere chat reads plain lines (no arrow keys or history recall).

## Slash commands

| Command | Action |
|---|---|
| `/help` | List the commands |
| `/exit`, `/quit` | End the session |
| `/cost` | Turns, steps, tokens and cost so far |
| `/context` | How the context window is used: the model and its limit, and the tokens in the system prompt, AGENTS.md, tool definitions (including MCP and plugin tools), skills, messages and a compaction summary. Free space and the distance to the auto-compact threshold (80%) are included. Category sizes are estimates (~4 characters per token). When the last turn reported usage and the conversation still matches that request, the total is that usage (plus an estimate of anything appended since) and the line says so |
| `/new`, `/clear` | Start a fresh conversation with a new checkpoint |
| `/save [path]` | Save the checkpoint now, or a copy to `path` |
| `/compact` | Compact the conversation now and show the estimated size before and after (or that nothing could be compacted). Refused with `--no-compact`. A model error is reported and leaves history unchanged |
| `/raw` | Toggle rendered Markdown off and on for this session |
| `/model [provider/model]` | Show the current model, its fallback chain and any role routes, or switch the main model for the rest of the session (history is kept). An invalid spec is refused and the current model stays. See [Switching mid-session](models.md#switching-mid-session) |

A message that really starts with `/` is sent with a doubled slash: `//etc/hosts is empty?` sends `/etc/hosts is empty?`.

## Files

| File | What |
|---|---|
| `.tinyagent/chats/<timestamp>.json` | The session checkpoint (a normal trajectory), saved after every step; `-o <path>` picks the path instead. Resume it with `tinyagent chat --resume <path> --cwd <dir>`, or view it with `--transcript` |
| `history/chat_history` | What you typed, for Up/Down recall, in `--cwd`. Directory mode 0700, file 0600, the last 1000 entries. A message that starts with a space isn't recorded. `--no-chat-history` or config `chat_history: false` turns it off |

The history file holds your messages in plain text. To keep it out of Git, add `history/` to your `.gitignore`; tinyagent doesn't edit it for you.

## How it behaves

- Ctrl-C cancels a running turn (if any) and exits with status 130. The checkpoint is saved and can be resumed. Ctrl-D and `/exit` quit with status 0.
- Each message is one turn. `--max-turns`, `--cost-limit`, `--token-limit` and `--timeout` apply to each turn, not the whole session. A turn that hits a limit stops; your next message carries on.
- Pasting several megabytes of clipboard text (up to 8 MiB) is kept intact and sent as one message. If that message is huge relative to the model's context window (an estimate of about 4 characters per token, at least half the window), chat warns but still sends it.
- Replies stream as they arrive. After each turn a dim line shows its steps, tokens and cost, and the session total.
- `--ask-user` works: a question ends the turn, and your next message is the answer.
- `ask` permission rules prompt on the terminal, as in a normal run; see [Permissions](permissions.md#permissions).
- `--resume` of an unfinished run asks whether to continue that task first (`[Y/n]`); a finished run goes straight to the prompt.
- Chat needs a terminal on stdin and stdout and refuses otherwise (exit 2); use `-t` for scripts. It can't be combined with `--acp`, `--json`, workflows, `--eval`, review flags, `--rewind`, `--transcript`, `--output-schema`, `--check`, `--worktree`, `--attempts`, `--image` or `-t -` (exit 2).
- `--snapshots` is off in chat.
