# xbot

AI-powered bug fix bot — triggered from X, powered by coding agents.

A user reports a bug on X. You reply `fix: description`. A coding agent fixes it and opens a PR. That's it.

## Quick Start

**1. Install**

```bash
# One-liner (Linux/macOS)
curl -sSfL https://raw.githubusercontent.com/Shallum99/xbot/main/install.sh | sh

# Or with Go
go install github.com/Shallum99/xbot@latest
```

**2. Get your X API credentials**

Go to [developer.x.com](https://developer.x.com), create an app, and enable OAuth 2.0. Copy your **Client ID** and **Client Secret**.

**3. Authenticate**

```bash
xbot auth --client-id YOUR_CLIENT_ID --client-secret YOUR_CLIENT_SECRET
```

This opens your browser to authorize xbot with your X account.

**4. Set up the bot**

Run this from your project directory:

```bash
xbot init --handle your_x_handle
```

That's all you need — it defaults to Claude Code as the agent, `fix:` as the trigger, and the current directory as the repo. See `xbot init --help` for all options.

**5. Start monitoring**

```bash
xbot start
```

Now reply to any bug tweet with `fix: login page crashes on empty email` and the bot handles the rest.

## How It Works

```
Bug tweet on X
       |
You reply: "fix: login crashes on mobile"
       |
xbot polls X (every 60s) ──→ detects your "fix:" reply
       |
Fetches parent tweet (bug report + screenshots)
       |
Downloads media attachments
       |
Runs coding agent (Claude Code / Codex / Gemini)
       |
PR created on GitHub
```

No separate bot account needed. xbot uses your own X authentication — if you can tweet, xbot can monitor your tweets.

## Features

**Agent-agnostic** — Ships with Claude Code, Codex, and Gemini CLI support. Bring your own agent with `--agent custom --agent-cmd "my-tool"`.

**Media-aware** — Downloads screenshots and images from bug tweets and passes them to the coding agent for visual context.

**Skill file** — Drop a `.xbot.md` in your repo root with project-specific instructions. The agent reads it before fixing bugs.

**Security hardened** — SSRF protection on media downloads, command injection prevention, input validation, bounded resource usage, file locking, log sanitization.

**Polling with backoff** — Works with the free X API tier. Exponential backoff on rate limits, graceful shutdown on Ctrl+C.

**One bot per founder** — Uses your own OAuth2 token. No shared accounts, no access lists. Your auth is your identity.

## Prerequisites

1. **X API credentials** — Create an app at [developer.x.com](https://developer.x.com) with OAuth 2.0 enabled

2. **A coding agent** — At least one of:
   - [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude`) — default
   - [Codex](https://github.com/openai/codex) (`codex`)
   - [Gemini CLI](https://github.com/google-gemini/gemini-cli) (`gemini`)
   - Any custom CLI tool

## Commands

### `xbot auth`

Authenticate with the X API. Saves your credentials and runs the OAuth2 flow in your browser.

```bash
xbot auth --client-id YOUR_ID --client-secret YOUR_SECRET
```

You only need to run this once. Re-run it to update credentials or re-authenticate.

### `xbot init`

Configure the bot. Creates `~/.xbot`. Only `--handle` is required.

```bash
# Minimal — defaults to current dir, Claude Code, "fix:" trigger
xbot init --handle shallum

# Customize
xbot init --handle shallum --repo /path/to/project --agent codex --trigger "bug:"
```

| Flag | Default | Description |
|---|---|---|
| `--handle` | *(required)* | Your X handle, without the @ |
| `--repo` | `.` | Path to the git repo the agent will fix bugs in |
| `--agent` | `claude` | Which coding agent to use: `claude`, `codex`, `gemini`, `custom` |
| `--agent-cmd` | | Command to run when `--agent=custom` |
| `--trigger` | `fix:` | The keyword you'll reply with on X to trigger a fix |
| `--poll-interval` | `60s` | How often to check X for new trigger tweets |
| `--branch-prefix` | `bot/fix-` | Prefix for git branches created by the agent |
| `--dry-run` | `false` | Log what would happen without running the agent |

### `xbot start`

Start the polling loop. Runs until you hit Ctrl+C.

```bash
xbot start                    # run forever
xbot start --once             # poll once and exit
xbot start --dry-run          # log what would happen
xbot start --poll-interval 30s
```

### `xbot run <tweet>`

Process a single tweet. Useful for testing.

```bash
xbot run 1234567890
xbot run https://x.com/user/status/1234567890
xbot run 1234567890 --dry-run
```

### `xbot status`

Show current config and polling state.

```bash
$ xbot status
Bot Configuration
  Handle:         @shallum
  Trigger:        fix:
  Repo:           /home/user/my-project
  Agent:          claude
  Poll Interval:  1m0s
  Branch Prefix:  bot/fix-

Bot State
  Since ID:       2026004777931280575
  Last Poll:      2026-02-23T14:07:04-06:00
  Processed:      3 tweet(s)
```

## Configuration

### Config file (`~/.xbot`)

```yaml
handle: your_handle
trigger_keyword: "fix:"
repo: /absolute/path/to/project
agent: claude
poll_interval: 1m0s
branch_prefix: bot/fix-
dry_run: false
```

### Skill file (`.xbot.md`)

Place a `.xbot.md` file in your repo root to give the coding agent project-specific instructions. This gets prepended to the agent's prompt.

```markdown
# Bug Fix Rules

- Never modify `core/engine.py` without running `pytest tests/`
- All API endpoints must be async
- Run `npm run lint` before committing
```

Keep it short — just guardrails. The agent explores the codebase on its own.

### Custom agents

Any CLI tool that reads a prompt from stdin and prints output to stdout works:

```bash
xbot init --handle you --agent custom --agent-cmd "my-agent --auto"
```

The prompt is passed via:
- **stdin** — piped directly
- **`XBOT_PROMPT` env var** — available in the subprocess environment

The agent should create a branch, fix the bug, push, and create a PR. xbot extracts the PR URL from stdout via regex.

## State

xbot tracks its polling progress in `~/.xbot-state`:

- **`since_id`** — Last tweet ID processed (for incremental polling)
- **`processed_ids`** — Map of tweet ID to status (`success`, `failed`, `skipped`)
- **`last_poll_time`** — Timestamp of last poll

State auto-prunes to 1000 entries. File writes use advisory locking to prevent corruption.

## Security

xbot is designed to be safe for long-running, unattended use:

- **SSRF protection** — Media downloads restricted to `twimg.com` HTTPS hosts with private IP blocking
- **No command injection** — Custom agents use `exec.Command` (no `sh -c`), prompts via env var / stdin
- **Input validation** — Handle format, trigger keyword safety, repo path checks, numeric since_id
- **Bounded resources** — 50MB media cap, 10KB skill file cap, 1000-entry state pruning
- **No auth leakage** — Verbose mode force-disabled in bot commands
- **File locking** — Advisory locks on state writes
- **Log sanitization** — Control characters stripped from all output

## Build from source

```bash
git clone https://github.com/Shallum99/xbot.git
cd xbot
go build -o xbot .
go test ./...
```

## Contributing

Contributions welcome. Please open an issue first to discuss what you'd like to change.

## License

MIT
