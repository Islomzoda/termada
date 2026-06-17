# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

## [0.7.4] — 2026-06-17

### Added
- **Homebrew**: the release now publishes the formula to the `Islomzoda/homebrew-tap`
  tap (using a `HOMEBREW_TAP_TOKEN` PAT), so `brew install islomzoda/termada/termada`
  works.

## [0.7.3] — 2026-06-17

### Fixed
- **Remote SSH sessions broke on any host with `tmux` installed.** The 0.7.1
  reconnect feature `exec`'d the remote shell into a tmux session when tmux was
  present, but tmux's screen rendering corrupts the `\036`-delimited completion
  markers → `session init timed out`. (This is why CI failed only on
  `ubuntu-latest`, which ships tmux, while macOS and local Debian passed.) Removed
  the tmux wrapping: remote sessions are a plain SSH PTY shell; `Reconnect`
  re-dials a fresh shell (the in-flight command is orphaned, not preserved). The
  release test gate is strict again (no more `continue-on-error`).

### Added
- **Onboarding CLI:** `termada doctor` (one-command health check), `termada
  service install|uninstall|status` (launchd/systemd auto-start), `termada
  dashboard` (print/open the URL), `termada setup` now actually runs `claude mcp
  add`, and `termada vault reset` for a forgotten passphrase.
- **No-Go installer:** `curl … | sh` downloads the prebuilt binary (SHA-256
  verified) — no Go required.
- **Dashboard:** token-less local-trust access on your own machine, a 📜
  History/replay timeline over the hash-chained audit (with filter), a read-only
  Policies panel, and a status legend.
- **SSH agent / on-disk-key auth:** a server you can already `ssh` into needs no
  stored credential — no vault, no passphrase.

## [0.7.2] — 2026-06-16

Agent-experience pass — make the MCP surface light on tokens and self-explaining
for agents, and the dashboard clearer for humans.

### Changed
- **Lean agent responses.** `exec_run`/`exec_poll`/`exec_list`/`file_read` now emit
  only fields that carry signal — zero/false/redundant values are dropped (no more
  echoing back the command, or `hold_input/hold_output/awaiting_input:false/…` on
  every call). A finished `exec_run` returns `{status, exit_code, stdout,
  session_id}` instead of a 12-field blob; a job handle (`job_id`/`next_cursor`)
  appears only while there's something left to poll. The rich structs still flow
  to the dashboard/control-plane unchanged.
- **`exec_list` is now newest-first, capped, and deterministic** (was unbounded in
  randomized map order): default 20, `limit` up to 200, `filter` active|recent|all,
  plus an `omitted` count when older jobs are hidden. The newest-first ordering
  also fixes the dashboard's job list order.
- **Self-explaining tool descriptions.** Spell out the argv-is-not-a-shell-line
  boundary (`$VAR`/`|`/`&&`/globs are literal → use `["bash","-lc","…"]`), that
  omitting `session` uses a persistent per-agent default session, and that
  `file_*` operate on the daemon host (not session-scoped). `capabilities` returns
  a one-shot `quickstart` cheatsheet.

### Added
- **Actionable error hints.** Structured errors now carry a `hint` — a one-line
  recovery step (e.g. `session_busy` → run in another session or kill the current
  job; `cursor_expired` → re-poll from an empty cursor) so agents recover in one
  shot instead of guessing.
- **Dashboard status legend** — a plain-language "what do the statuses mean?"
  explainer (orphaned/backgrounded/awaiting_input/…) for non-technical operators.
- First tests for the MCP tool layer (lean shapes, hints, newest-first, quickstart).

## [0.7.1] — 2026-06-16

Completes the multi-agent hardening items from the 0.7.0 road-to-1.0 pass.

### Added
- **Per-agent concurrency quotas** (MA-3): `defaults.max_jobs_per_agent` caps the
  number of concurrent jobs a single agent may hold; the next job is refused with
  `parallelism_exceeded` instead of letting one agent starve the others. Counted
  across all of the agent's sessions; `0` means unlimited.
- **Non-spoofable agent identity** (MA-2): an agent may be bound to a secret token
  via `agents: [{ id, token }]`. The stdio shim presents it with `serve --stdio
  --token <t>` (or `$TERMADA_AGENT_TOKEN`), sent as the `X-Termada-Agent-Token`
  header; the daemon resolves the token to its configured agent id and ignores any
  self-asserted `owner`. Without a configured token, the self-asserted id is used
  (local/dev mode), exactly as before.
- **Remote-session reconnect** (RM-3): persistent SSH sessions are wrapped in a
  named `tmux` session, so a dropped connection is transparently re-dialled and
  re-attached — cwd/env/running command survive on the server. The in-flight job
  is orphaned across the gap (its result can't be trusted) and the session keeps
  serving new commands. Falls back to a plain shell on hosts without tmux (the
  connection still re-establishes; prior state is not preserved). *Reconnect
  recovery is unit-tested with a real shell under a simulated drop.*

## [0.7.0] — 2026-06-16

The "road to 1.0" pass — closing the gaps from the self-review.

### Added
- **Persistent remote SSH sessions** (§14/P-10): `session_create target=<server>`
  opens a live shell over SSH (vault creds, TOFU host key) running the same
  marker-based exec protocol as local — cwd/env persist across commands, and the
  agent gets the full exec/poll/write/kill surface remotely. Session transport is
  now an interface (local PTY or SSH) behind one engine. *Integration-tested
  against an in-process SSH server with a real bash PTY.*
- **Execution heuristics** (§13): adaptive per-class timeouts (build/test/install/
  db/network) and auto-backgrounding of long-running daemons (dev servers,
  watchers, `compose up`, `tail -f`) instead of blocking; on timeout a job is left
  running, not killed.
- **Policy auto-answers** (IN-2) to confirmed prompts; **no-newline prompts now
  surface immediately** to `awaiting_input` (fixed a reader hold-back bug).
- **Agent observability**: online/offline, connection counts, jobs/sessions,
  denied, last command + history; auto-detected agent names from MCP clientInfo.
- **Audit log rotation** with verifiable sealed segments + **disk-full fail-closed**
  for dangerous commands (RE-4/RE-7).
- **Job GC** (EX-9), **Prometheus `/metrics`** (§8.6).
- **Distribution**: Dockerfile (build-verified), `.deb`/`.rpm` + Homebrew via
  goreleaser; **docs**: threat model (SECURITY.md) and plugin guide.
- Integration tests for the control-plane HTTP API.

### Fixed
- Dashboard terminals (token gating broke vendored xterm), default port moved off
  macOS AirPlay's :7000 to :7717, daemon double-bind now fatal.

## [0.6.0] — 2026-06-16

Agent observability and automatic server status.

### Added
- **Agent panel** (spec MA-1/MA-2): the dashboard shows each connected agent —
  auto-detected by name from MCP `clientInfo` (e.g. `claude-code`, `cursor`) — with
  connection count, jobs run, sessions opened, denied commands, last command and
  last-seen time. Sessions and jobs are attributed to the owning agent. The engine
  keeps a per-agent registry; the shim records a connection on `initialize`
  (`/api/agent/connect`).
- **Automatic server health checks**: the daemon health-checks every configured
  server every 30s (runs `true` over SSH) and caches the result, so the dashboard
  shows online/offline status with a colored dot without anyone clicking. Manual
  per-server **test** still available.

## [0.5.1] — 2026-06-16

Dashboard fixes (found by driving it in a real browser) and UI server management.

### Fixed
- **Terminals never rendered in the browser**: the page loads with `?token=`, but
  the browser fetches sub-resources (vendored `xterm.js`) relative — without the
  token — so they 401'd and `Terminal` was undefined, breaking every terminal on
  click. The token now gates only `/api/*`; static dashboard assets are served
  freely on loopback (anti-rebinding Host/Origin checks still apply).
- **Default dashboard port 7000 collided with macOS AirPlay Receiver** (which
  listens on `*:7000` over IPv6), so browsers resolving `localhost`→`::1` hit
  AirPlay and showed an error page. Default moved to `127.0.0.1:7717`.

### Added
- **Session terminals**: clicking a session opens one continuous live terminal
  for the whole shell (output across all its jobs), with operator input.
- **Server management from the dashboard** (human-only, never an MCP tool —
  SEC-7): add a server (name/host/user/tags + SSH key or password), with the
  credential stored in the vault and the server persisted (`servers.json`,
  hot-reloaded, no restart); a live connectivity **test** (ok / unreachable /
  denied …) with a status dot; and remove. The vault is created on first use from
  the dashboard, so no terminal step is required.
- Operator takeover backend: per-job `hold_input`/`hold_output`, human-aware
  `Write`/`Poll`, SSE output streams for jobs and sessions.

## [0.5.0] — 2026-06-16

Real-time terminal cockpit — the human-observability pillar done properly
(spec §8.1/§8.2/OB-1/OB-2).

### Added
- **Live terminal dashboard**: each session/job renders as a real terminal
  (xterm.js, vendored locally so it works offline), streaming output live over
  Server-Sent Events (`/api/exec/stream`) instead of list polling. Tabs per job,
  activity feed, approval cards, Stop-All.
- **Operator takeover** (§ human intervention): from the dashboard a person can
  **type directly into a job's PTY**; **hold agent input** (`/api/exec/hold` →
  agent writes rejected with `denied_by_policy`); and **pause the output the
  agent receives** while still watching the live stream. Engine tracks
  `hold_input`/`hold_output` per job; `Write`/`Poll` are human-aware.

### Tests
- Engine: human-takeover input block + human input, output-hold pauses agent only.
- All packages pass under the race detector; native + Windows cross-compile green.


## [0.4.0] — 2026-06-16

The previously-deferred phase 3 & 4 work — recovery, snapshots, plugins,
self-update, Windows cross-compilation.

### Added
- **Crash recovery** (§21/RE-1/RE-2): the job registry is persisted as an
  atomically-written WAL; on restart, jobs that were running are honestly
  recovered as `orphaned` (a local PTY process can't survive a parent crash —
  fork R1), while gracefully-finished jobs keep their terminal status.
- **Snapshots / undo** (§19, narrow per R8): `termada snapshot create|list|restore`
  takes a bounded copy of a local file/dir and restores it over the original via
  an atomic swap. Explicitly scoped to local FS — no general undo for
  database/network/remote effects.
- **Self-update** (DI-3): `termada update` checks GitHub releases, downloads the
  platform asset, verifies its SHA-256 against the published checksums, and
  atomically replaces the binary. goreleaser config + a tag-triggered release
  workflow produce the cross-platform archives (signing gated on later keys).
- **Plugins** (§29): out-of-process plugin executables are discovered from the
  plugins dir, queried for their tools, and surfaced to agents over MCP as
  `<plugin>.<tool>`. Plugins run with a minimal environment — no vault, audit key
  or dashboard token (capability boundary, §3a).
- **Windows cross-compilation**: the tree now builds for windows/amd64 (and
  linux/darwin × amd64/arm64). The ConPTY PTY backend and Windows signals are
  honest "not supported yet" stubs (fork R6) until that platform work lands.

### Tested
- SSH is now exercised end-to-end against an in-process SSH server (auth
  success/denial, exec, exit codes, TOFU host keys, via fleet) — no longer
  compile-only.

## [0.3.0] — 2026-06-16

Remote access, files, recipes and notifications — the rest of phase 2.

### Added
- **Fleet / remote** (§14/§15): `fleet_run` executes a command across servers
  selected by name or tag, with a bounded-parallelism aggregator and per-server
  structured results (ok / nonzero_exit / unreachable / timeout / conn_lost /
  denied) and a summary; `server_list` exposes the inventory with no secrets. SSH
  backend uses `golang.org/x/crypto/ssh` with credentials from the vault and TOFU
  host-key verification (rejects mismatches). *Live SSH needs a reachable server;
  the selection/aggregation logic is unit-tested via a mock runner.*
- **Vault unlock in the daemon** (§CR-5): `termada unlock` sends the passphrase to
  the running daemon, which holds the key in memory and registers secret values
  with the redactor so they can never echo back to an agent.
- **File tools** (§16): `file_read` (with best-effort redaction + size limit) and
  `file_write`.
- **Recipes** (§19/RC-1): `recipe_list` and `recipe_run` execute configured
  command macros step-by-step, each step policy-checked, stopping on failure.
- **Notifications** (§8.3/OB-7): desktop (osascript / notify-send) and optional
  Telegram on approval-needed, denial, failed jobs and agent connects.
- CLI: `servers`, `unlock`.

### MCP tools (18)
exec_run, exec_start, exec_poll, exec_write, exec_signal, exec_kill, exec_list,
session_create, session_list, session_close, logs_tail, file_read, file_write,
recipe_list, recipe_run, server_list, fleet_run, capabilities.

## [0.2.0] — 2026-06-16

Phase 2 + the phase-1 daemon pillar. termada is now a long-lived daemon with a
live web dashboard, human-in-the-loop approvals, an encrypted vault and a
tamper-evident audit log.

### Added
- **Daemon + control-plane** (spec R4): `termada serve` runs a long-lived process
  exposing an HTTP/JSON control-plane over a Unix socket (local trust) and the
  dashboard over loopback TCP. `termada serve --stdio` is now a thin shim that
  proxies MCP to the daemon (auto-spawning it; falling back to in-process if
  unavailable), enabling multi-agent attribution and a shared dashboard.
- **Live web dashboard** (§8.1): real-time sessions, jobs, activity feed (SSE),
  pending approvals with Approve/Deny, and a Stop-All kill-switch. Token auth with
  anti-DNS-rebinding (loopback Host/Origin) checks (§M12).
- **Policy engine** (§18): argv-level allow/deny/confirm classification with
  hot-reloadable named policies, per-agent policy mapping.
- **Confirmation queue** (§18a): dangerous commands park in `awaiting_confirmation`
  with a `confirmation_id`, resolved by a human via dashboard/CLI; deny-by-default
  timeout. The agent channel cannot self-approve.
- **Encrypted vault** (§17): age-based, CGO-free credential store with a CLI
  (`termada vault init|set|list|rm`). Secrets never returned to agents (§3a).
- **Tamper-evident audit log** (§8.5/SEC-3): hash-chained, fsynced, secret-redacted;
  `termada audit verify` detects any alteration.
- **Event bus** (§8.7) feeding dashboard (best-effort) and audit (durable).
- **CLI**: `status`, `jobs`, `sessions`, `logs`, `kill`, `stop`, `pending`,
  `approve`, `deny`, `audit [verify]`, `top` (live TUI), `vault`, `setup`.
- **Config** (§24): YAML config with defaults, policies, agents, redaction.

### Changed
- MCP clients now launch `termada serve --stdio` (the shim).

## [0.1.0] — 2026-06-16

First usable release: a local command-execution engine for AI agents, exposed
over MCP. Scope is phase 1 of the spec (see `docs/tz/Termada-TZ.md` §30); the
long-lived daemon, dashboard/TUI, SSH/fleet, vault and policy engine are later
phases.

### Added
- Persistent-shell sessions over a real PTY that preserve `cwd`, env and venv
  between commands (SS-1/SS-3), one foreground command at a time (SS-5).
- Async job model: `exec_start` returns a `job_id` immediately; `exec_poll`
  fetches incremental output by a stable, opaque cursor with gap detection
  (EX-2/EX-3/§11a).
- `exec_run` convenience with timeout; `mode=background` hands control back after
  a short grace instead of blocking (EX-7).
- `exec_write` sends input to the job's PTY (so `sudo`/`ssh`/`read` prompts work),
  with `secret=true` redaction (EX-4/IN-3/§3a).
- `exec_signal` / `exec_kill` deliver signals to the running command's process
  group, isolated from the session shell via job control (EX-5/§18b).
- Output processing: stateful ANSI/VT cleaning with chunk-boundary carry,
  carriage-return collapse, bounded retention, and best-effort secret redaction
  (OUT-3/OUT-5).
- Full job status enum and a structured error catalog (§22a/§22b).
- A dependency-light MCP server over newline-delimited JSON-RPC on stdio with 12
  tools: `exec_run`, `exec_start`, `exec_poll`, `exec_write`, `exec_signal`,
  `exec_kill`, `exec_list`, `session_create`, `session_list`, `session_close`,
  `logs_tail`, `capabilities`.
- `install.sh`, Makefile, and CI (vet + race tests + build on Linux and macOS).

### Security
- Commands are passed as an argv array and quoted so shell metacharacters are
  inert (R3). The honest threat-model boundaries are documented in spec §3a.

### Known limitations (tracked for later phases)
- No long-lived daemon yet: each MCP client runs its own in-process engine, so
  the dashboard/TUI and cross-agent views are not available (phase 2).
- `exec_run` auto-mode does not yet auto-detect long-running daemons; use
  `exec_start` or `mode=background` for non-blocking execution.
- Windows is not yet supported (no ConPTY); macOS and Linux only.
- No on-disk registry persistence / crash recovery yet (phase 3).
