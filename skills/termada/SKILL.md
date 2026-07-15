---
name: termada
description: Use the termada MCP tools to run terminal commands reliably — persistent local & remote-SSH sessions that keep cwd/env, async long-running jobs (dev servers, builds), answering interactive prompts, human-approval for dangerous commands, and clean structured output. Use whenever running shell commands or working on a remote server, instead of a raw shell or a raw `ssh` client.
---

# Using termada

termada exposes terminal execution over MCP. The tools are available once the
server is registered; this skill explains how to drive them well. **MCP
registration alone is enough — this skill just improves how you use it.** Call
`capabilities()` once for asserted client id, tool/mode availability, remote
support and a one-line `quickstart`; use `server_list()` for the registered
inventory. A configured transport token remains authoritative if it resolves a
different owner than the asserted client id.

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
yet. Setting it up takes ~2 minutes and is user-wide (do it once per user
account, every project gets it). Help the user through these steps; repo & docs
are at <https://github.com/Islomzoda/termada>.

1. **Install the binary** — skip if `termada --version` already works. On macOS
   or Linux `amd64`/`arm64`:
   ```
   curl -fsSL https://raw.githubusercontent.com/Islomzoda/termada/main/install.sh | sh
   ```
   (alternative: `brew install Islomzoda/tap/termada`; Windows uses a manual
   release archive because the native PTY/ConPTY runtime is not implemented. The
   Docker image runs a standalone daemon/dashboard and is not a drop-in host
   stdio MCP registration; see the repository README).
2. **Connect it over MCP** — add one MCP server, globally so every project sees
   it. The server is just `command: "termada"`, `args: ["serve","--stdio"]`:
   - **Claude Code:** `claude mcp add --scope user termada -- termada serve --stdio`
     (or add a top-level `"mcpServers": { "termada": { … } }` entry to `~/.claude.json`).
   - **Cursor / Windsurf / other MCP clients:** add the same `{command, args}`
     under `mcpServers` in that client's settings (user / global scope, not per-project).
3. **(optional) Install this skill globally** so the agent always has the guidance:
   copy this `SKILL.md` to the agent's user skills dir — for Claude Code that's
   `~/.claude/skills/termada/SKILL.md`.
4. **Restart the agent** so it loads the new MCP server, then call
   `capabilities()` to verify the MCP connection. `termada doctor` checks the
   local runtime/daemon, not whether the agent loaded its MCP registration.

After this there's no per-project setup and nothing to copy each time. The daemon
starts automatically when the agent connects; `termada dashboard --open` opens
the human dashboard.

## Pick the right call

- **Quick command, want the result now:** `exec_run` with `command` as an argv
  array, e.g. `{"command":["ls","-la"]}`. It waits up to `timeout_ms` and returns
  `{status, exit_code, stdout}` (empty/false fields are omitted to stay light).
- **Long-running (dev server, `docker compose up`, watcher):** use `exec_start`
  (returns a `job_id` immediately) or `exec_run` with `mode:"background"`. Read
  output incrementally with `exec_poll(job_id, cursor)`, passing back the
  `next_cursor` each time. A response cap is a sequential page, not a tail skip:
  `truncated:true` with `has_more:true` means call again with that cursor, even if
  the job is already terminal. Stop only when the status is terminal and
  `has_more` is absent. `gap:true` means older bytes expired from retention and
  cannot be recovered; continue from the returned cursor. `truncated:true`
  without `has_more` on an initial `exec_run` likewise means older retained bytes
  were already lost, not that another page exists. A background job still
  occupies its session; create a dedicated session if other commands must run
  concurrently.
- **Interactive prompt** (`[Y/n]`, password, `read`): when a poll shows
  `status:"awaiting_input"` (the `prompt` is included), send input with
  `exec_write(job_id, input)`. For passwords set `secret:true` so the exact value
  is registered for redaction and omitted from normal input logging.
- **Stop something:** `exec_kill(job_id)` or `exec_signal(job_id, "SIGINT")`.
  Local PTY jobs receive real process-group signals. In a remote SSH session,
  supported kill/term/int requests all reduce to best-effort PTY Ctrl-C; verify
  the remote process stopped instead of assuming a force-kill succeeded.
- **Lost track of a job:** `exec_list(filter)` (`active` | `recent` | `all`)
  returns the known jobs with their `job_id` and `status`.

## Sessions preserve state

Commands run in a persistent shell, so `cd` and `export` persist across calls.
Omit `session` and your per-agent default session is used (state still persists).
Create a named session with `session_create` and pass its `session_id` when you
want a SECOND independent shell (a separate cwd/venv), or a remote one
(`target=<server>`). Set the optional `workspace` label when parallel projects
should stay distinct in the operator dashboard. Only one job runs per session
at a time, including a job
whose response was backgrounded; a second concurrent call returns
`session_busy`. Either wait or use another session. Each owner may have at most
32 sessions, with 128 total in the daemon. Close one you no longer need with
`session_close`.

## Remote servers

To work on a remote box, do **not** shell out to a raw `ssh` client — go through
termada so the session is observable, reconnecting, and policy-gated. The server
must be registered first (in `config.yaml` `servers:` or the dashboard's
**Servers → Add**); then:

- open a remote shell with `session_create(target="<server-name>")` and run
  `exec_run`/`exec_start` in that `session_id` — state persists while connected;
  a dropped link is re-dialled as a fresh shell, so prior cwd/env are lost. The
  in-flight job becomes `orphaned`, but its uncontrolled remote process may still
  continue; verify remote state before retrying a non-idempotent operation;
- or run one command across servers with
  `fleet_run(command=[...], servers=["<name>"])` (or `tags=[...]`), which returns
  structured per-server results. The command must be a non-empty argv array;
  all fleet calls share a daemon-wide ceiling of 5 active targets, and requested
  parallelism can reduce this call's concurrency but cannot raise that ceiling.
  A call matches at most 256 targets and returns at most 2 MiB of aggregate
  stdout/stderr/error text; inspect each result's `truncated` flag.

`server_list()` shows what's registered. If the target server isn't there, ask
the human to register it (config or dashboard) rather than falling back to `ssh`.

## Port forwards

Use `port_forward(server, remote_host, remote_port)` for an SSH local-to-remote
tunnel (for example, a database listening on the remote server's
`127.0.0.1:5432`). The returned `local_addr` is on the **daemon host**, is
loopback-only for agent calls, and stays live until `port_forward_close(id)`.
Opening is policy-gated and rolled back unless its start audit record is durable.
Live forwards are capped at 16 per owner and 64 daemon-wide.
`port_forward_list()` returns only this agent's forwards; close is owner-scoped
and emits a lifecycle event but is not re-evaluated by command policy. Each
forward accepts at most 64 simultaneous connections. Close a forward as soon as
the work using it is done.

## Dangerous commands wait for a human

Some commands are gated by policy. Two outcomes to handle:

- **`status:"awaiting_confirmation"`** (with a `confirmation_id`): a human must
  approve it. **Surface this in the chat** — tell the user, in your reply, what
  the command will do and that it needs their approval (in the dashboard/CLI),
  rather than silently looping. Then poll the `job_id` until it runs/completes or
  becomes `failed` (the human rejected it, or approval timed out and denied by
  default).
  You **cannot** approve through the agent API, so don't try to work around the
  gate; just relay it to the human and wait.
- **`error.code:"denied_by_policy"`**: the command is refused outright. This is
  final — read `error.hint`, adjust, and don't try to bypass it (e.g. don't
  re-encode the same action to dodge the rule).

## Files

`file_read` / `file_write` are session-aware (pass absolute paths, not relative
to a session's cwd): omit `session`, or pass the `session_id` of a local session,
to act on the **daemon host**. Do not pass the literal string `"local"` as an id.
With a **remote** session's id they transfer UTF-8 text over SFTP without
invoking a shell. They are not arbitrary-binary APIs. Local host paths are shared
across agents rather than owner-namespaced, so stay within the current project.
`file_read` returns a prefix (100,000 bytes by default, at most 1 MiB);
`truncated:true` means the remainder was not returned and there is no file cursor.
When the daemon uses `security.run_as`, local file tools fail closed so they
cannot bypass the dropped shell uid; use `exec_run` in a local session for file
access. Remote SFTP file operations remain available.
On the daemon host, secret paths are refused with `denied_by_policy`: the
daemon's own runtime dir (tokens, vault, audit) and the host credential stores
(`~/.ssh`, `~/.aws`, `~/.gnupg`). Don't try to read those.

## Recipes

`recipe_list()` shows named command macros; `recipe_run(name)` runs one. Each
step is policy-checked and records a durable execution intent before touching
the shell, so a recipe can still park a step for approval.

## Conventions

- `command` is always an **argv array** (`["git","status"]`), never a shell
  string — `$VAR`, `|`, `&&`, `>`, globs and `cd x && y` are literal, not
  operators. For shell features use `["bash","-lc","<line>"]`; that payload is
  still policy-checked, and ambiguous compound syntax fails closed under
  deny/confirm policies. Never reshape a denied command to evade that check.
- Terminal output is ANSI-cleaned and best-effort redacted; `file_read` and
  `fleet_run` output are also best-effort redacted. Plugin JSON results are not
  automatically redacted. Treat plugins as trusted local executables and never
  ask them to return credentials.
- Errors come back structured as `{error:{code,message,retriable,hint}}` — read
  `hint` for the one-step recovery (e.g. `session_busy`, `not_found`,
  `parallelism_exceeded`, `denied_by_policy`).
