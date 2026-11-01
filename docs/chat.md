# Interactive chat

`tinyagent chat` (or `tinyagent -i`) opens a conversation on the terminal: type a message, press Enter, read the reply, type the next one. Every message continues the same conversation, with the same tools, permissions, sandbox and config as a normal run.

```sh
tinyagent chat                          # start a session in the current directory
tinyagent chat -t "explain main.go"     # send a first message, then keep chatting
tinyagent chat -- explain main.go       # the same, without quoting
tinyagent chat --resume .tinyagent/chats/20261031-115411.json --cwd .   # pick a session back up
```

Press Ctrl-D (or type `/exit`) to quit. tinyagent prints the session's turns and cost, and the command that resumes it.

## What it looks like

Replies are Markdown, rendered for the terminal as each block completes: headings, **bold**, *italic*, `code`, fenced code in a box with its language and light highlighting, nested and task lists, blockquotes, tables, rules, and links with their URL. Text wraps to the terminal width (up to 120 columns). Tool calls collapse into one line per step; a spinner shows while the model thinks.

```text
tinyagent v0.15.0 · interactive chat
  model  openai/gpt-4o
  cwd    ~/src/app
  saves  .tinyagent/chats/20261031-115411.json
Type a message and press Enter. /help lists commands, Ctrl-C stops a turn, Ctrl-D quits.

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

## Keys

| Key | Action |
|---|---|
| Enter | Send the message |
| `\` at the end of a line | Continue the message on the next line |
| `"""` | Start or end a multi-line block |
| Up / Down (Ctrl-P / Ctrl-N) | Previous / next message from history |
| Left / Right, Home / End, Ctrl-A / Ctrl-E | Move the cursor |
| Ctrl-U / Ctrl-K / Ctrl-W | Delete to the start / to the end / the previous word |
| Ctrl-C while the model works | Stop that turn; the conversation stays, and you can keep typing |
| Ctrl-C with text on the line | Clear the line |
| Ctrl-C twice at an empty prompt (within 2s) | Quit |
| Ctrl-D at an empty prompt | Quit |

Line editing works on Linux and macOS. Elsewhere chat reads plain lines (no arrow keys or history recall).

## Slash commands

| Command | Action |
|---|---|
| `/help` | List the commands |
| `/exit`, `/quit` | End the session |
| `/cost` | Turns, steps, tokens and cost so far |
| `/new`, `/clear` | Start a fresh conversation with a new checkpoint |
| `/save [path]` | Save the checkpoint now, or a copy to `path` |
| `/compact` | Compact the conversation before the next request (refused with `--no-compact`) |
| `/raw` | Toggle rendered Markdown off and on for this session |
| `/playbook <name>` | Follow [playbook](playbooks.md) `<name>` from `./playbooks` for the next message only (`tinyagent chat --playbook <name>` does the same for the first message) |

A message that really starts with `/` is sent with a doubled slash: `//etc/hosts is empty?` sends `/etc/hosts is empty?`.

## Files

| File | What |
|---|---|
| `.tinyagent/chats/<timestamp>.json` | The session checkpoint (a normal trajectory), saved after every step; `-o <path>` picks the path instead. Resume it with `tinyagent chat --resume <path> --cwd <dir>`, or view it with `--transcript` |
| `history/chat_history` | What you typed, for Up/Down recall, in `--cwd`. Directory mode 0700, file 0600, the last 1000 entries. A message that starts with a space isn't recorded. `--no-chat-history` or config `chat_history: false` turns it off |

The history file holds your messages in plain text. To keep it out of Git, add `history/` to your `.gitignore`; tinyagent doesn't edit it for you.

## How it behaves

- Each message is one turn. `--max-turns`, `--cost-limit`, `--token-limit` and `--timeout` apply to each turn, not the whole session. A turn that hits a limit stops; your next message carries on.
- Replies stream as they arrive. After each turn a dim line shows its steps, tokens and cost, and the session total.
- `--ask-user` works: a question ends the turn, and your next message is the answer.
- `ask` permission rules prompt on the terminal, as in a normal run; see [Permissions](permissions.md#permissions).
- `--resume` of an unfinished run asks whether to continue that task first (`[Y/n]`); a finished run goes straight to the prompt.
- Chat needs a terminal on stdin and stdout and refuses otherwise (exit 2); use `-t` for scripts. It can't be combined with `--acp`, `--json`, workflows, `--eval`, review flags, `--rewind`, `--transcript`, `--output-schema`, `--check`, `--worktree`, `--attempts`, `--image` or `-t -` (exit 2).
- `--snapshots` is off in chat.
