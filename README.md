# Symphony (Go)

A minimal Go implementation of the [OpenAI Symphony spec](https://github.com/openai/symphony/blob/main/SPEC.md), adapted to use `kimi-cli` as the coding agent instead of Codex.

## Philosophy

- **Linear is the UI** — work status lives in Linear where your team already looks.
- **Logs are the debugger** — structured `key=value` logs to stdout/stderr. Ask an agent to parse them if you need a summary.
- **No web dashboard, no HTTP API** — just a daemon and logs.

## Features

- Polls Linear for active issues
- Creates isolated per-issue workspaces
- Runs `kimi-cli --yolo` in each workspace
- Handles retries with exponential backoff
- Stall detection and process termination
- Hot reload of `WORKFLOW.md` without restart
- `max_turns` defaults to **500** (instead of 20)

## Requirements

- Go 1.21+
- `kimi-cli` installed and in your PATH
- Linear API key (`LINEAR_API_KEY` env var)

## Build

```bash
cd symphony
go build -o symphony .
```

## Configure

Copy `WORKFLOW.md.example` to `WORKFLOW.md` and customize:

```bash
cp WORKFLOW.md.example WORKFLOW.md
```

Key settings:

```yaml
tracker:
  api_key: $LINEAR_API_KEY      # env var reference
  project_slug: my-project      # your Linear project slug

agent:
  max_turns: 500                # max kimi-cli invocations per issue

codex:
  command: kimi-cli --yolo      # agent command (configurable)
  turn_timeout_ms: 3600000      # 1 hour max per turn
  stall_timeout_ms: 300000      # 5 min with no output = kill
```

The Markdown body after the YAML front matter is the prompt template, rendered with Go's `text/template`. Available variables:

- `{{.issue.Identifier}}`
- `{{.issue.Title}}`
- `{{.issue.Description}}`
- `{{.issue.Priority}}`
- `{{.issue.Labels}}`
- `{{.issue.BlockedBy}}`
- `{{.attempt}}` (null on first run, integer on retries)

## Run

```bash
export LINEAR_API_KEY="lin_api_..."
./symphony
```

Or specify a custom workflow path:

```bash
./symphony -workflow /path/to/WORKFLOW.md
```

## Logs

All events are structured `key=value` lines:

```
time=2026-04-28T15:00:00Z level=info msg="dispatching" issue_id="abc123" issue_identifier="MT-649"
time=2026-04-28T15:00:01Z level=info msg="turn_starting" issue_id="abc123" turn="1" workspace_path="/tmp/symphony_workspaces/MT-649"
time=2026-04-28T15:05:00Z level=info msg="turn_completed" issue_id="abc123" turn="1" exit_code="0"
```

Tail logs and ask an agent for summaries or debugging.

## Architecture

```
main.go         → CLI entrypoint, signal handling
config.go       → WORKFLOW.md loader + hot reload
workflow.go     → Prompt template rendering
linear.go       → Linear GraphQL client
orchestrator.go → Poll loop, dispatch, retry, reconciliation
workspace.go    → Directory lifecycle + hooks
agent.go        → kimi-cli subprocess runner
logger.go       → Structured key=value logs
state.go        → In-memory orchestrator state machine
```

## Deviations from Spec

| Spec | This Implementation |
|---|---|
| Codex app-server | `kimi-cli` subprocess (one turn per invocation) |
| max_turns default 20 | **500** |
| HTTP API / dashboard | **None** (logs only) |
| thread_id/turn_id sessions | Not applicable; workspace files serve as persistent state |
