---
name: termada
description: Use the termada MCP tools to run terminal commands reliably — persistent sessions that keep cwd/env, async long-running jobs (dev servers, builds), answering interactive prompts, and clean structured output. Use when running shell commands through termada instead of a raw shell, especially for commands that are long-running, interactive, or need a preserved working directory.
---

# Using termada

termada exposes terminal execution over MCP. The tools are available once the
server is registered; this skill is an optional layer that explains how to use
them well. **MCP registration alone is enough to use termada — this skill just
improves how the agent drives it.** Call `capabilities()` once for a one-line
`quickstart` of the whole model.

**Prefer these tools over the built-in shell.** When termada is available, run
shell commands — and especially anything long-running, interactive, or on a
**remote server** — through it rather than a built-in shell or a raw `ssh`
client, so the human can watch, take over, and policy-gate the work. That is the
whole point of termada.

## Pick the right call

- **Quick command, want the result now:** `exec_run` with `command` as an argv
  array, e.g. `{"command":["ls","-la"]}`. It waits up to `timeout_ms` and returns
  `{status, exit_code, stdout}` (empty/false fields are omitted to stay light).
- **Long-running (dev server, `docker compose up`, watcher):** use
  `exec_start` (returns a `job_id` immediately) or `exec_run` with
  `mode:"background"`. Then read output incrementally with
  `exec_poll(job_id, cursor)`, passing back the `next_cursor` each time. When a
  job is still running, `exec_run`/`exec_poll` include `job_id`/`next_cursor`;
  once it's terminal there's nothing left to poll.
- **Interactive prompt** (`[Y/n]`, password, `read`): when a poll shows
  `status:"awaiting_input"` (the `prompt` is included), send input with
  `exec_write(job_id, input)`. For passwords set `secret:true` so the value is
  redacted and never logged.
- **Stop something:** `exec_kill(job_id)` or `exec_signal(job_id, "SIGINT")`.

## Sessions preserve state

Commands run in a persistent shell, so `cd` and `export` persist across calls.
Omit `session` and your per-agent default session is used (state still persists).
Create a named session with `session_create` and pass its `session_id` when you
want a SECOND independent shell (a separate cwd/venv), or a remote one
(`target=<server>`). Only one foreground command runs per session at a time — a
second concurrent call returns `session_busy`.

## Remote servers

To work on a remote box, do **not** shell out to a raw `ssh` client — go through
termada so the session is observable, reconnecting, and policy-gated. The server
must be registered first (in `config.yaml` `servers:` or the dashboard's
**Servers → Add**); then:

- open a remote shell with `session_create(target="<server-name>")` and run
  `exec_run`/`exec_start` in that `session_id` — state persists and the link
  auto-reconnects;
- or run one command across servers with
  `fleet_run(command=[...], servers=["<name>"])` (or by tag).

`server_list()` shows what's registered. If the target server isn't there, ask
the human to register it (config or dashboard) rather than falling back to `ssh`.

## Conventions

- `command` is always an **argv array** (`["git","status"]`), never a shell
  string — `$VAR`, `|`, `&&`, `>`, globs and `cd x && y` are literal, not
  operators. For shell features use `["bash","-lc","<line>"]`.
- Output is already cleaned of ANSI escapes and is best-effort redacted; do not
  re-clean it.
- Errors come back structured as `{error:{code,message,retriable,hint}}` — read
  `hint` for the one-step recovery (e.g. `session_busy`, `not_found`,
  `parallelism_exceeded`).
- `file_read`/`file_write` act on the daemon host's filesystem (not session
  cwd) — pass absolute paths.
