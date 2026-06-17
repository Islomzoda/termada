<h1 align="center">Termada</h1>

<p align="center"><b>The reliable, transparent terminal runtime for AI agents.</b></p>

<p align="center">
  <a href="https://github.com/Islomzoda/termada/releases"><img alt="Release" src="https://img.shields.io/github/v/release/Islomzoda/termada?color=2ea043&label=release"></a>
  <a href="https://github.com/Islomzoda/termada/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Islomzoda/termada/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://registry.modelcontextprotocol.io"><img alt="MCP Registry" src="https://img.shields.io/badge/MCP-registry-5b5bd6"></a>
  <img alt="Go" src="https://img.shields.io/badge/go-1.26%2B-00ADD8">
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

Termada is a single-binary, local-first runtime that sits between an AI agent and
the terminal — local, and remote over SSH. The agent talks to it over the
[Model Context Protocol](https://modelcontextprotocol.io) and gets a sturdy
toolset instead of a raw shell: commands that don't hang, persistent sessions
that keep `cwd`/env, async jobs with streamed output, PTY input for interactive
prompts, and structured results — while you watch and control everything from a
live dashboard with a kill-switch and an approval queue.

<p align="center">
  <img alt="Termada — the live dashboard in action" src="docs/demo.gif" width="760">
</p>

<p align="center"><sub>The live dashboard: every session is a real terminal you can watch and take over — block or pause the agent, type in yourself — beside the agent panel, read-only policies, a tamper-evident History, and a Stop-All kill-switch. The same view, in full detail:</sub></p>

<p align="center">
  <img alt="Termada dashboard — live sessions, operator take-over, approvals, audit" src="docs/dashboard.png" width="860">
</p>

---

## Why

Handing an AI agent a raw shell is fragile and opaque: a command blocks on a
prompt and the agent hangs; `cd` and exported env vanish between calls; long
builds flood the context window; and you can't see — let alone stop — what's
running. Termada replaces the raw shell with a runtime that is **reliable** for
the agent and **transparent** for you:

- **Reliable for the agent** — every command has a deadline and returns
  structured output; sessions persist `cwd`/env; long jobs run async and stream
  incrementally instead of dumping; interactive prompts are answerable.
- **Transparent for you** — one dashboard shows every agent and every session as
  a real terminal; dangerous commands wait for your approval; nothing is hidden;
  one button stops everything.

## Features

**Execution engine**
- Persistent-shell sessions over a PTY that keep `cwd`, env, and venv between commands.
- Async jobs: `exec_start` → `job_id`; poll incrementally by a stable cursor, with a full status state machine and structured errors.
- Answer interactive prompts (`exec_write`, with secret redaction); send signals / kill by process group.
- Clean output: stateful ANSI/VT stripping, CR-collapse, bounded retention, best-effort secret redaction.

**Live control & observability**
- A long-lived daemon with a control plane over a Unix socket; `serve --stdio` is a thin shim that proxies MCP to it — so **multiple agents share one daemon and one dashboard**.
- Web dashboard where **each session renders as a real terminal** (xterm.js, streamed over SSE) with **operator take-over**: type into a job's PTY, hold the agent's input, or pause its output.
- Approval queue, activity feed, read-only policy view, and a **Stop-All** kill-switch.
- A TUI (`termada top`) and a full inspection CLI.
- Tamper-evident, hash-chained, secret-redacted audit log (`termada audit verify`).

**Security**
- Policy engine: argv-level allow / deny / confirm. Dangerous commands park in an approval queue (deny-by-default on timeout; agents can't self-approve).
- age-encrypted vault (no CGO); secrets are never returned to agents, only injected into the daemon.
- Per-agent quotas and non-spoofable identity: cap concurrent jobs per agent, and bind an agent id to a secret token so `owner` can't be forged.

**Remote & fleet**
- Persistent **remote SSH sessions** with transparent reconnect — a dropped link is re-dialled so the session keeps serving commands.
- `fleet_run` across servers by name or tag with structured per-server results; SSH via vault creds, ssh-agent, or on-disk keys, with TOFU host-key pinning.

**Operations**
- Crash recovery (jobs persist; running jobs come back as `orphaned`), local-FS snapshots/undo, desktop & Telegram notifications.
- Out-of-process plugins exposed to agents as `<plugin>.<tool>`.
- `termada update` — self-update from GitHub releases (download → verify SHA-256 → atomic replace).

> **Not yet:** a native Windows ConPTY runtime (cross-compiles today, but PTY and
> signals are stubs) and code-signing / notarization. See the
> [roadmap](docs/tz/Termada-TZ.md).

## Install

**One line, no Go needed** — downloads the prebuilt binary for your OS/arch
(SHA-256 verified) to `~/.local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/Islomzoda/termada/main/install.sh | sh
```

Pin a version with `TERMADA_VERSION=v0.7.5`, or change the location with
`TERMADA_BIN_DIR=~/bin`. If `~/.local/bin` isn't on your `PATH`, the installer
prints the one line to add.

<details><summary>Other ways — Docker, Homebrew, packages, source</summary>

```bash
# Docker (nothing to install) — pull the published image:
docker run --rm -p 7717:7717 ghcr.io/islomzoda/termada serve

# Homebrew:
brew install Islomzoda/tap/termada

# From source (needs Go 1.26+):
TERMADA_FROM_SOURCE=1 ./install.sh
# or:  go build -o ~/.local/bin/termada ./cmd/termada
```

Releases also ship `.deb` and `.rpm` packages on the
[releases page](https://github.com/Islomzoda/termada/releases).

</details>

## Quick start

```bash
termada serve         # start the daemon + dashboard (prints the URL)
termada dashboard     # open it — http://127.0.0.1:7717, no token on your own machine
```

Connect it to your agent — for Claude Code:

```bash
claude mcp add termada -- termada serve --stdio
```

Or drop it into a project `.mcp.json` (see [`.mcp.json.example`](.mcp.json.example)):

```json
{ "mcpServers": { "termada": { "command": "termada", "args": ["serve", "--stdio"] } } }
```

Then just ask the agent to do terminal work — it flows through Termada while you
watch and control it live. One daemon is shared across every agent session and
shows them all on the same dashboard.

<details><summary>Install as a Claude Code plugin</summary>

This repo is also a Claude Code plugin marketplace — it bundles the MCP server
config and the usage skill (you still need the `termada` binary on `PATH`):

```text
/plugin marketplace add Islomzoda/termada
/plugin install termada@termada
```

</details>

<!-- mcp-name: io.github.Islomzoda/termada -->

## MCP tools

Commands are passed as an **argv array** (`["echo", "hi"]`), never a shell string,
so shell metacharacters are inert by construction.

| Group | Tools |
| --- | --- |
| Run | `exec_run` · `exec_start` · `exec_poll` · `exec_write` · `exec_signal` · `exec_kill` · `exec_list` |
| Sessions | `session_create` · `session_list` · `session_close` |
| Files & logs | `file_read` · `file_write` · `logs_tail` |
| Recipes | `recipe_list` · `recipe_run` |
| Remote | `server_list` · `fleet_run` |
| Meta | `capabilities` |

## CLI

```text
termada serve [--stdio]              daemon, or the MCP shim
termada dashboard | top | status     open the UI / live TUI / overview
termada jobs [-f] | sessions         list jobs / sessions
termada logs <job> [-f]              stream a job's output
termada kill <job> | stop            kill a job / kill-switch (stop all)
termada pending | approve | deny     human-in-the-loop approvals
termada audit [verify]               audit feed / verify the tamper chain
termada servers | unlock             remote inventory / unlock the vault
termada vault init|set|list|rm|reset manage credentials
termada snapshot create|list|restore local-FS safety net (undo)
termada doctor                       health check
termada service install|uninstall    run the daemon at login
termada update                       self-update from GitHub releases
```

## Documentation

- [docs/SECURITY.md](docs/SECURITY.md) — threat model: what's protected and what isn't.
- [docs/PLUGINS.md](docs/PLUGINS.md) — writing out-of-process tool plugins.
- [docs/PUBLISHING.md](docs/PUBLISHING.md) — release & MCP-registry process.
- [docs/tz/Termada-TZ.md](docs/tz/Termada-TZ.md) — full product specification / roadmap.

## Architecture

A single daemon owns all state; agents connect through a stdio shim, and you
observe through the dashboard, TUI, or CLI — all over the same control plane.

```text
cmd/termada            CLI: daemon, shim, inspection/control, vault
internal/engine        sessions, jobs, PTY, status machine, signals, files, recipes
internal/output        cursor buffers, VT cleaner, redaction
internal/policy        argv allow/deny/confirm classification
internal/vault         age-encrypted credential store
internal/audit         hash-chained tamper-evident log
internal/bus           event bus (observability + durable audit)
internal/daemon        long-lived process: listeners, auth, lifecycle
internal/controlplane  HTTP/JSON control-plane server + client
internal/dashboard     embedded web UI
internal/tui           termada top
internal/fleet         server selection + concurrent aggregation
internal/sshx          SSH runner (vault / agent / key auth, TOFU host keys)
internal/mcp           MCP JSON-RPC stdio server + tools
```

## Development

```bash
make vet test    # vet + tests
make race        # tests under the race detector
```

Engine tests exercise a real PTY and `bash`; fleet logic is unit-tested with a
mock runner; the daemon stack and SSH are integration-tested end-to-end.

## License

[Apache-2.0](LICENSE).
