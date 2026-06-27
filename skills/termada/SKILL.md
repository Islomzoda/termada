---
name: termada
description: Use the termada MCP tools to run terminal commands reliably — persistent local & remote-SSH sessions that keep cwd/env, async long-running jobs (dev servers, builds), answering interactive prompts, human-approval for dangerous commands, and clean structured output. Use whenever running shell commands or working on a remote server, instead of a raw shell or a raw `ssh` client.
---

# Using termada

termada exposes terminal execution over MCP. The tools are available once the
server is registered; this skill explains how to drive them well. **MCP
registration alone is enough — this skill just improves how you use it.** Call
`capabilities()` once for a one-line `quickstart` plus your `allowed`/`denied`
policy summary and the registered `servers`.

**Default to termada for ALL shell work — do not use the built-in terminal.**
Whenever termada is connected, run every command through `exec_run`/`exec_start`
instead of the built-in shell, and never shell out to a raw `ssh` client. This is
the default, not a preference: routing everything through termada is what lets the
human watch the work live, take over, and policy-gate it — that is the whole point.
The only time to fall back to a built-in shell is if termada's tools are genuinely
not available (see Setup below). Anything long-running, interactive, or on a
**remote server** especially must go through termada.

## Setup — if the tools aren't there yet (one-time)

Don't see termada's tools (`exec_run`, `session_create`, …)? It isn't connected
yet. Setting it up takes ~2 minutes and is machine-wide (do it once, every
project gets it). Repo & docs: <https://github.com/Islomzoda/termada>. Help the
user through these steps — they work for any agent:

1. **Install the binary** — skip if `termada --version` already works:
   ```
   curl -fsSL https://raw.githubusercontent.com/Islomzoda/termada/main/install.sh | sh
   ```
   (alternatives: `brew install Islomzoda/tap/termada`, or the Docker image
   `ghcr.io/islomzoda/termada`).
2. **Connect it over MCP** — add one MCP server, globally so every project sees
   it. The server is just `command: "termada"`, `args: ["serve","--stdio"]`:
   - **Claude Code:** `claude mcp add --scope user termada -- termada serve --stdio`
     (or add a top-level `"mcpServers": { "termada": { … } }` entry to `~/.claude.json`).
   - **Cursor / Windsurf / other MCP clients:** add the same `{command, args}`
     under `mcpServers` in that client's settings (user / global scope, not per-project).
3. **(optional) Install this skill globally** so the agent always has the guidance:
   copy this `SKILL.md` to the agent's user skills dir — for Claude Code that's
   `~/.claude/skills/termada/SKILL.md`.
4. **Restart the agent** so it loads the new MCP server, then verify with
   `termada doctor` (or call `capabilities()` once connected).

After this there's no per-project setup and nothing to copy each time. The daemon
starts automatically when the agent connects; `termada serve` + `termada dashboard`
open the human dashboard.

## Pick the right call

- **Quick command, want the result now:** `exec_run` with `command` as an argv
  array, e.g. `{"command":["ls","-la"]}`. It waits up to `timeout_ms` and returns
  `{status, exit_code, stdout}` (empty/false fields are omitted to stay light).
- **Long-running (dev server, `docker compose up`, watcher):** use `exec_start`
  (returns a `job_id` immediately) or `exec_run` with `mode:"background"`. Read
  output incrementally with `exec_poll(job_id, cursor)`, passing back the
  `next_cursor` each time. While a job runs, `exec_run`/`exec_poll` include
  `job_id`/`next_cursor`; once it's terminal there's nothing left to poll.
- **Interactive prompt** (`[Y/n]`, password, `read`): when a poll shows
  `status:"awaiting_input"` (the `prompt` is included), send input with
  `exec_write(job_id, input)`. For passwords set `secret:true` so the value is
  redacted and never logged.
- **Stop something:** `exec_kill(job_id)` or `exec_signal(job_id, "SIGINT")`.
- **Lost track of a job:** `exec_list(filter)` (`active` | `recent` | `all`)
  returns the known jobs with their `job_id` and `status`.

## Sessions preserve state

Commands run in a persistent shell, so `cd` and `export` persist across calls.
Omit `session` and your per-agent default session is used (state still persists).
Create a named session with `session_create` and pass its `session_id` when you
want a SECOND independent shell (a separate cwd/venv), or a remote one
(`target=<server>`). Only one foreground command runs per session at a time — a
second concurrent call returns `session_busy`; either wait, or use another
session. Close one you no longer need with `session_close`.

## Remote servers

To work on a remote box, do **not** shell out to a raw `ssh` client — go through
termada so the session is observable, reconnecting, and policy-gated. The server
must be registered first (in `config.yaml` `servers:` or the dashboard's
**Servers → Add**); then:

- open a remote shell with `session_create(target="<server-name>")` and run
  `exec_run`/`exec_start` in that `session_id` — state persists and a dropped
  link is auto-reconnected;
- or run one command across servers with
  `fleet_run(command=[...], servers=["<name>"])` (or `tags=[...]`), which returns
  structured per-server results.

`server_list()` shows what's registered. If the target server isn't there, ask
the human to register it (config or dashboard) rather than falling back to `ssh`.

## Dangerous commands wait for a human

Some commands are gated by policy. Two outcomes to handle:

- **`status:"awaiting_confirmation"`** (with a `confirmation_id`): a human must
  approve it. **Surface this in the chat** — tell the user, in your reply, what
  the command will do and that it needs their approval (in the dashboard/CLI),
  rather than silently looping. Then poll the `job_id` until it turns `running`
  (approved) or `denied`/`failed` (rejected, or it timed out — which denies by
  default). You **cannot** approve your own command, so don't try to work around
  the gate; just relay it to the human and wait.
- **`error.code:"denied_by_policy"`**: the command is refused outright. This is
  final — read `error.hint`, adjust, and don't try to bypass it (e.g. don't
  re-encode the same action to dodge the rule).

## Files

`file_read` / `file_write` act on the **daemon host's** filesystem (not the
session cwd) — pass absolute paths. They are session-aware: pointing them at a
**remote** session is refused (`not_supported`) so they never silently touch the
local host — read/write remote files with `exec_run` in that server's session
(`["cat","<path>"]` / `["tee","<path>"]`). Secret paths are refused with
`denied_by_policy`: the daemon's own runtime dir (tokens, vault, audit) and the
host credential stores (`~/.ssh`, `~/.aws`, `~/.gnupg`). Don't try to read those.

## Recipes

`recipe_list()` shows named command macros; `recipe_run(name)` runs one. Each
step is policy-checked and audited individually, so a recipe can still park a
step for approval.

## Conventions

- `command` is always an **argv array** (`["git","status"]`), never a shell
  string — `$VAR`, `|`, `&&`, `>`, globs and `cd x && y` are literal, not
  operators. For shell features use `["bash","-lc","<line>"]`.
- Output is already cleaned of ANSI escapes and is best-effort redacted; do not
  re-clean it.
- Errors come back structured as `{error:{code,message,retriable,hint}}` — read
  `hint` for the one-step recovery (e.g. `session_busy`, `not_found`,
  `parallelism_exceeded`, `denied_by_policy`).
