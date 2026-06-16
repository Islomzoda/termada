# Termada

**The reliable, transparent terminal runtime for AI agents.**

Termada is a single-binary, local-first runtime that sits between an AI agent and
the terminal (local now, remote over SSH in a later phase). The agent talks to it
over the [Model Context Protocol](https://modelcontextprotocol.io) and gets a
sturdy toolset instead of a raw shell: commands that don't hang, persistent
sessions that keep `cwd`/env, async jobs with streamed output, PTY input for
interactive prompts, structured results — and (coming next) a live dashboard so a
human can see and stop everything in real time.

> Status: **0.x, phase 1 (local engine).** See [docs/tz/Termada-TZ.md](docs/tz/Termada-TZ.md)
> for the full product spec and the phased roadmap (§30). License: Apache-2.0.

## What works today (phase 1)

- Persistent-shell sessions over a PTY that preserve `cwd`, env and venv (SS-1/SS-3).
- Async job model: `exec_start` returns a `job_id` immediately; poll incrementally by cursor (EX-2/EX-3).
- Convenience `exec_run` with timeout and auto-background of long-running commands (EX-7).
- PTY input for prompts via `exec_write`, with `secret` redaction for passwords (EX-4/IN-3).
- Signals / kill by process group, with clean job-control isolation (EX-5/§18b).
- Output processing: stateful ANSI/VT cleaning, CR-collapse, head/tail retention, best-effort secret redaction (OUT-3/OUT-5).
- Full job status enum and a structured error catalog (§22a/§22b).
- A dependency-light MCP server over newline-delimited JSON-RPC on stdio (§22).

Not yet (later phases): the long-lived daemon + dashboard/TUI, SSH/fleet, vault,
policy engine, tamper-evident audit, Windows parity. The code is structured so the
engine becomes the core the daemon hosts.

## Quick start

Install the binary (needs Go 1.26+):

```bash
./install.sh          # builds and installs to ~/.local/bin/termada
# or:  make install   # same, honours BIN_DIR=
# or:  make build     # just ./bin/termada
```

Register it with your MCP client.

**Claude Code:**
```bash
claude mcp add termada -- ~/.local/bin/termada serve
```
or drop a `.mcp.json` at your project root (copy `.mcp.json.example`); use the
absolute path to the binary if `~/.local/bin` is not on your PATH:
```json
{ "mcpServers": { "termada": { "command": "/Users/you/.local/bin/termada", "args": ["serve"] } } }
```

**Cursor / Claude Desktop:** add the same `mcpServers` block to the client's MCP
config file.

Verify it works by hand over stdio:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"cli","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"exec_run","arguments":{"command":["echo","hi from termada"]}}}' \
  | termada serve
```

You should see an `initialize` reply and a tool result containing
`"stdout": "hi from termada\n"`.

## Tools (phase 1)

`exec_run` · `exec_start` · `exec_poll` · `exec_write` · `exec_signal` ·
`exec_kill` · `exec_list` · `session_create` · `session_list` · `session_close` ·
`logs_tail` · `capabilities`

Commands are passed as an **argv array** (e.g. `["echo","hi"]`), not a shell
string: arguments are safely quoted so shell metacharacters are inert (spec R3).

## Layout

```
cmd/termada        CLI entrypoint (serve --stdio, version)
internal/engine    sessions, jobs, PTY, status state machine, signals
internal/output    cursor buffers, VT cleaner, best-effort redaction
internal/mcp       minimal MCP JSON-RPC stdio server + tools
internal/errs      structured error contract (§22b)
internal/ids       id / marker generation
docs/tz            product specification (Russian working spec)
```

## Development

```bash
make vet test       # vet + tests
make race           # tests under the race detector
```

The engine tests exercise a real PTY and `bash`, so they are integration tests by
nature (spec RL-1).
