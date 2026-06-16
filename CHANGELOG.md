# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

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
