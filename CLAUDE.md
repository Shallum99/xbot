# xbot

AI-powered bug fix bot triggered from X. Standalone Go CLI that imports xurl as a library.

## Architecture

```
main.go          → Cobra CLI (init, start, run, status commands)
bot/api.go       → X API request construction (SearchTriggerTweets, FetchTweet)
bot/config.go    → BotConfig struct, YAML persistence (~/.xbot), Validate()
bot/state.go     → BotState (since_id, processed_ids), file locking, auto-pruning
bot/tweet.go     → ParsedTweet, ParseSearchResponse, FetchParentTweet, ExtractBugDesc
bot/agent.go     → Agent interface + ClaudeAgent, CodexAgent, GeminiAgent, CustomAgent
bot/handler.go   → Pipeline: fetch parent → download media → run agent → log PR
bot/poller.go    → Polling loop with exponential backoff + graceful shutdown
```

## Key Dependencies

- `github.com/xdevplatform/xurl/api` — Client interface, RequestOptions, NewApiClient
- `github.com/xdevplatform/xurl/auth` — Auth, NewAuth (OAuth2 token management)
- `github.com/xdevplatform/xurl/config` — Config, NewConfig (env var loading)
- `github.com/spf13/cobra` — CLI framework
- `gopkg.in/yaml.v3` — Config/state serialization

## How It Works

1. Founder replies to bug tweet with `fix: description`
2. Poller searches `from:handle "fix:"` with since_id via X API v2
3. Handler fetches parent tweet (bug report + media)
4. Media downloaded to temp dir (SSRF-safe, 50MB cap)
5. Coding agent runs as subprocess in repo directory
6. Agent output parsed for PR link via regex
7. State updated, PR link logged

## Config Files

- `~/.xbot` — Bot config (handle, trigger, repo, agent, interval)
- `~/.xbot-state` — Polling state (since_id, processed tweet IDs)
- `~/.xurl` — Auth tokens (managed by xurl, not us)
- `.xbot.md` — Per-repo skill file for agent instructions (max 10KB)

## Security Measures

- SSRF: Media downloads restricted to twimg.com + private IP blocking
- Injection: No sh -c, custom agents use exec.Command, prompt via stdin/env
- Validation: Handle regex, trigger keyword chars, repo path existence, numeric since_id
- Bounds: 50MB media, 10KB skill file, 1000-entry state pruning, field length limits
- Locking: syscall.Flock on state file writes
- Logs: Control characters stripped via unicode.IsControl

## Build & Test

```bash
go build ./...
go test ./...
```

## Conventions

- Go 1.24+
- No external test framework — standard `testing` package
- Cobra for CLI, YAML for config/state
- Error wrapping with fmt.Errorf("context: %w", err)
- Comments reference security issue IDs (C1, C2, H1-H4, M1-M6, L1-L5)
