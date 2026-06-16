---
name: termada
description: Use the termada MCP tools to run terminal commands reliably — persistent sessions that keep cwd/env, async long-running jobs (dev servers, builds), answering interactive prompts, and clean structured output. Use when running shell commands through termada instead of a raw shell, especially for commands that are long-running, interactive, or need a preserved working directory.
---

# Using termada

termada exposes terminal execution over MCP. The tools are available once the
server is registered; this skill is an optional layer that explains how to use
them well. **MCP registration alone is enough to use termada — this skill just
improves how the agent drives it.**

## Pick the right call

- **Quick command, want the result now:** `exec_run` with `command` as an argv
  array, e.g. `{"command":["ls","-la"]}`. It waits up to `timeout_ms` and returns
  `{status, exit_code, stdout, next_cursor}`.
- **Long-running (dev server, `docker compose up`, watcher):** use
  `exec_start` (returns a `job_id` immediately) or `exec_run` with
  `mode:"background"`. Then read output incrementally with
  `exec_poll(job_id, cursor)`, passing back the `next_cursor` each time.
- **Interactive prompt** (`[Y/n]`, password, `read`): when a poll shows
  `status:"awaiting_input"` (or you expect a prompt), send input with
  `exec_write(job_id, input)`. For passwords set `secret:true` so the value is
  redacted and never logged.
- **Stop something:** `exec_kill(job_id)` or `exec_signal(job_id, "SIGINT")`.

## Sessions preserve state

Commands run in a persistent shell, so `cd` and `export` persist across calls.
Create a named session with `session_create` and pass its `session_id` to keep a
working directory/venv; otherwise a per-agent default session is used. Only one
foreground command runs per session at a time — a second concurrent call returns
`session_busy`.

## Conventions

- `command` is always an **argv array** (`["git","status"]`), never a shell
  string. Arguments are quoted so `;`, `|`, `$()` etc. are literal, not operators.
- Output is already cleaned of ANSI escapes and is best-effort redacted; do not
  re-clean it.
- Errors come back structured as `{error:{code,message,retriable}}` — check
  `code` (e.g. `session_busy`, `not_found`, `parallelism_exceeded`).
- Call `capabilities()` to see the agent id, API version and available tools.
