# Web UI

`tinyagent web` runs [interactive chat](chat.md) in your browser. It starts a small local server on `127.0.0.1`, opens your default browser to it, and runs each message you send as one turn of the same conversation `tinyagent chat` would have. It uses the same tools, permissions, sandbox, lockdown, memory, AGENTS.md and config, and it saves the same checkpoints.

```sh
tinyagent web                         # serve on 127.0.0.1:7777 (or the next free port) and open the browser
tinyagent web --no-open               # print the URL instead of opening a browser
tinyagent web --port 8080             # use exactly this port; exit if it's busy
tinyagent web -m anthropic/claude-sonnet-4-5 --cwd ./service --sandbox
tinyagent web --resume .tinyagent/chats/20261031-115411.json --cwd .
```

Press Ctrl-C in the terminal to stop the server. A running turn is cancelled, its checkpoint is saved, and open tabs show a "connection lost" notice.

## Options

| Option | Default | What |
|---|---|---|
| `--port <n>` (`--web-port`) | `7777` | Port to listen on. Left unset, a busy 7777 falls back to the next free port (7778–7787), then to one the OS picks, and the URL printed shows the port used. Set explicitly, a busy port exits 1 with an error |
| `--no-open` (`--web-no-open`) | off | Print the URL and don't launch a browser |

Every chat flag applies: `-m`, `--profile`, `-c`, `--cwd`, `--resume`, `-o`, `--sandbox`, `--read-only`, `--lockdown`, `--memory`, the limits (per turn), and so on. `-t` sends a first message. A bare `tinyagent web` starts the server right away; `tinyagent web --help` prints its help. `--web` can't be combined with `-i`/`chat`, `--acp` or other modes that can't be combined with chat, and `--web-port`/`--web-no-open` exit 2 without `--web`.

The browser is opened with `open` on macOS, `xdg-open` on Linux and other Unix systems, and `rundll32 url.dll,FileProtocolHandler` on Windows. If that fails (for example over SSH with no display), the URL is printed so you can open it yourself, such as through an SSH port forward.

## Features

- **Conversation:** streaming Markdown replies with headings, lists, task lists, tables, quotes and links. Code blocks are syntax-highlighted (Go, JavaScript/TypeScript, Python, shell, Rust, C-family, SQL, diffs) and each has a Copy button. `/raw` shows the plain text instead.
- **Tool calls:** each one appears as a collapsible card with its name and a short argument summary. Expanded, it shows the full arguments, the result (errors highlighted) and a coloured diff for edits. Running `bash` and configured command tools automatically open a live-output preview, replaced by the final result when done. SSE `tool_output` events contain `{id, name, output}`. A [CLI handoff](models.md#cli-handoff-providers) provider emits the same `tool_output` events for the CLI's own tools; a card is opened for them even though the saved reply has no TinyAgent tool call. Previews use configured secret redaction, are capped at 256 KiB per call, and are not saved or replayed on reconnect. `--no-stream` disables them; bash `stream: false` silences one call.
- **Permission prompts:** when a `permissions` rule says `ask`, the turn pauses and an inline card offers **Allow once**, **Always allow** (for the session) and **Deny**, with an optional reason that goes back to the model. `ask_user` questions get the same kind of card, with their options as buttons.
- **Stop:** the Stop button, Esc or `/stop` interrupts the running turn, like Ctrl-C in chat.
- **Edit and regenerate:** edit the last message (↑ in an empty box, or the Edit button on it) and resend it, or regenerate the last reply. Either one replaces the latest turn in the conversation. Files the turn already changed aren't reverted.
- **Model picker:** the model box in the top bar or `/model <provider/model>` switches the model for the next turn. Suggestions list the providers' known models and any configured custom providers. A model that doesn't resolve is refused, and the current one is kept. The starting model is resolved the same way as for chat: `-m`, then `$AGENT_MODEL` (from the environment or `./.env`, where `model=` is an alias), then the config's `model`. If the model is a [CLI handoff provider](models.md#cli-handoff-providers) (`claude-code`, `codex-exec`, `opencode`), the status bar shows a `handoff` badge. Assistant text streams as SSE `delta` events while the CLI runs, and the CLI's own tool activity streams as `tool_output`. The saved reply is still only the CLI's final answer: TinyAgent's tools, sandbox and subagents don't apply, and there are no permission prompts. `--no-stream` waits for that answer and sends neither `delta` nor `tool_output`.
- **Sessions sidebar:** a searchable list of the saved conversations in `./.tinyagent/chats`. Opening one shows it, and your next message continues it in a new checkpoint. **New chat** (or `/new`) starts fresh.
- **Project panel:** shows the AGENTS.md files and project memory entries the session loaded.
- **Status bar:** the model, `--cwd`, turns, tokens, cost, the checkpoint path, and sandbox, read-only, lockdown and permissions badges.
- **Files and images:** drag files onto the page, paste them, or use the paperclip. Images are attached to the message like `--image`. Text files up to 512 KB are added to the message as fenced blocks.
- **Command discovery:** typing `/` opens an autocomplete menu of slash commands. Ctrl/⌘-K opens a command palette (actions, commands and models), and `?` lists every shortcut.
- **Layout:** follows the system light or dark mode, which the theme button can override (saved in the browser). The layout is responsive, so on narrow screens the sidebar becomes an overlay. The view auto-scrolls while you're at the bottom and shows a **Jump to latest** button when you scroll up.
- **Reconnect:** if the event stream drops, a banner says so and the page keeps retrying. When it's back, the conversation is reloaded from the server.
- **Accessibility:** semantic landmarks and labels, a live region for new messages, visible focus, keyboard access to every control, arrow-key navigation in menus and lists, and reduced motion when the system asks for it.

## Slash commands

The same as [chat](chat.md#slash-commands), plus a few for the browser.

| Command | Action |
|---|---|
| `/help` | Commands and keyboard shortcuts |
| `/new`, `/clear` | Start a fresh conversation with a new checkpoint |
| `/cost` | Steps, tokens and cost so far |
| `/save [path]` | Save the checkpoint now, or a copy to `path` (relative to `--cwd`) |
| `/compact` | Compact the conversation before the next request (refused with `--no-compact`) |
| `/raw` | Toggle rendered Markdown off and on |
| `/model [provider/model]` | Open the model picker, or switch to the given model |
| `/history` | Search saved conversations |
| `/stop` | Interrupt the running turn |
| `/exit`, `/quit` | Stop the `tinyagent web` server |

`//text` sends a message that starts with `/`, as in chat.

## Keyboard shortcuts

| Keys | Action |
|---|---|
| Enter | Send |
| Shift-Enter | New line |
| ↑ (in an empty box) | Edit the last message |
| `/` (outside the box) | Focus the message box |
| Ctrl/⌘-K | Command palette |
| Ctrl-Shift-O | New chat |
| Ctrl-Shift-M | Model picker |
| Ctrl-Shift-F | Search conversations |
| Ctrl-Shift-S | Show or hide the sidebar |
| Ctrl-Shift-C | Copy the last reply |
| Esc | Stop the running turn, or close a menu or dialog |
| `?` (outside the box) | Show shortcuts |
| ↑ / ↓, Enter, Tab | Move through and pick from the slash menu, palette or sessions list |

## Security

The UI is meant for one person on their own machine.

- The server binds `127.0.0.1` only, never a public interface.
- Each run makes a random 192-bit token, which is in the URL it opens or prints. Every API request and the event stream need it. The page moves it from the URL into the tab's session storage and removes it from the address bar. Requests without it get 401.
- The `Host` header must be `127.0.0.1:<port>`, `localhost:<port>` or `[::1]:<port>`, which defeats DNS rebinding. Requests carrying an `Origin` from any other site are refused with 403, and POSTs must be `application/json`, so a web page can't submit a form to it.
- API keys, `.env` values and the environment never reach the browser. The state the page gets has the model name, `--cwd`, settings flags, AGENTS.md text, memory entries and the conversation, but no credentials. The page has a strict Content-Security-Policy with no inline scripts and no third-party requests.
- The session honours the same sandbox, `--read-only`, `--lockdown`, permission rules and `ask` prompts as chat. The browser can't turn any of them off.

Anyone who has the URL with its token can drive the agent until the server stops, so don't share it.

## Gaps compared with terminal chat

- **Line editor:** terminal-only features such as Ctrl-R search, persistent `history/chat_history` recall and readline keys aren't needed, since the browser's text box and the sessions list cover them. Web messages aren't added to `history/chat_history`.
- **Several tabs:** they all share one session. Only one turn runs at a time, and a second send gets "busy".
- **Edit and regenerate:** these work on the latest turn of the current conversation, and don't undo file changes (use [`--rewind`](rewind.md) for that).
- **Model switch:** it changes the main model only. Subagent, auxiliary and delegate models keep their configured values.

## How it's built

Plain `net/http` with the page embedded through `go:embed` (`cmd/tinyagent/web/`). It's hand-written HTML, CSS and JavaScript, including a small Markdown renderer and highlighter, so there's no Node build step, no vendored library and no generated files. The browser gets events over Server-Sent Events (`GET /api/events`) and acts with JSON POSTs (`/api/message`, `/api/command`, `/api/cancel`, `/api/model`, `/api/permission`, `/api/regenerate`, `/api/history/open`).
