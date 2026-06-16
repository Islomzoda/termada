# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project aims to follow
[Semantic Versioning](https://semver.org/) once it reaches 1.0.

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
