# Termada plugins

Plugins add dynamic MCP tools without modifying Termada. A plugin is a **trusted
local executable**, not sandboxed code. Put it in
`~/.config/termada/plugins/`; its tools appear as `<plugin>.<tool>` after the
daemon restarts.

Installing a plugin grants it the daemon user's filesystem and network
permissions. Read the [security model](SECURITY.md) before enabling third-party
code.

## Protocol

Termada invokes a plugin in two forms:

```text
<plugin> describe
  -> stdout: {"tools":[{"name":"...","description":"...","inputSchema":{...}}]}

<plugin> call <tool>
  <- stdin:  tool arguments as JSON
  -> stdout: one JSON result
```

`inputSchema` must be a JSON Schema object with top-level `"type":"object"`.
Plugin and local tool names may contain only ASCII letters, digits, `_` and `-`;
Termada exposes each local name under the executable's base name. Duplicate or
undeclared tools are rejected.

Discovery runs once during daemon startup. Termada captures the executable's
file identity and SHA-256 digest before/after `describe` and checks both around
each call. A stable path replacement or in-place content change makes discovery
or the call fail until the daemon is restarted. This is TOCTOU hardening, not
code signing or an atomic containment boundary against a malicious writer that
can race execution; protect the plugin directory with OS permissions.

## Example

Create `~/.config/termada/plugins/greet` and make it executable:

```bash
#!/usr/bin/env bash
set -eu

case "${1:-}" in
  describe)
    printf '%s\n' '{"tools":[{"name":"hello","description":"Greet someone","inputSchema":{"type":"object","properties":{"who":{"type":"string"}}}}]}'
    ;;
  call)
    [ "${2:-}" = hello ] || exit 2
    who=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("who", "world"))')
    python3 -c 'import json,sys; print(json.dumps({"greeting": "hello " + sys.argv[1]}))' "$who"
    ;;
  *)
    exit 2
    ;;
esac
```

```bash
chmod 700 ~/.config/termada/plugins/greet
```

After restarting the daemon, the MCP tool is `greet.hello`.

On Windows, plugins must be regular `.exe` files. Unix executable and write mode
checks do not model Windows ACLs, so protect the plugin directory with an ACL
that only the daemon account can modify.

## Policy and audit

Each call is evaluated as the synthetic argv `plugin <plugin.tool>`. This lets a
policy gate one tool or a namespace, for example:

```yaml
policies:
  restricted:
    deny:
      - "plugin admin.*"
    confirm:
      - "plugin network.*"
```

An allow decision runs the call. A deny decision refuses it. Plugin calls do not
support the interactive approval queue, so a confirm decision also fails closed
and tells the caller to use an approved built-in workflow instead. An allowed
call does not start unless its attributed start event is durably appended. A
finished/failed outcome event is emitted afterward; if that append fails, audit
is latched unhealthy so later protected operations fail closed, but the completed
plugin side effects cannot be rolled back.

The `describe` invocation runs during daemon startup before an agent call exists;
it is not gated by an agent policy. This is another reason to install only trusted
executables.

## Execution limits

Termada applies process controls and resource bounds intended to reduce
accidental leaks and runaway helper processes:

| Limit | Value |
| --- | --- |
| `describe` wall time | 5 seconds |
| `call` wall time | 60 seconds |
| Tools per plugin | 128 |
| Plugin/local tool name | 64 bytes |
| Executable file | 64 MiB |
| JSON call input | 1 MiB |
| stdout | 1 MiB |
| stderr | 64 KiB |
| Concurrent calls | 8 daemon-wide |

A timeout terminates the main plugin process and attempts descendant cleanup: a
process group is killed on Unix, and a kill-on-close Job Object is assigned just
after process start on Windows. This cleanup is best-effort for trusted code; a
plugin can deliberately detach, and a Windows child spawned before Job Object
assignment can escape. Oversized output, non-zero exit, invalid JSON, an
undeclared tool or a changed executable is returned as a tool error.

Termada skips symlink and non-regular entries inside the plugin directory. On
Unix it also requires an executable that is not writable by group or others.
File identity and content digest are checked before and after discovery/calls.

## What the limits do not provide

The child environment is reduced to:

```text
PATH=/usr/bin:/bin
TERMADA_PLUGIN=1
```

This prevents accidental inheritance of daemon environment variables. It does
**not** prevent the executable from opening files, reading the Termada runtime
directory, connecting to the network, spawning subprocesses, or using any other
permission available to the daemon uid. There is no namespace, chroot, seccomp,
container, capability or syscall sandbox, and descendant cleanup is not a
containment boundary.

Plugin arguments and JSON results are application-defined. Process error text is
best-effort redacted before it reaches the agent/audit, but a successful JSON
result is returned without automatic redaction. Do not pass secrets to a plugin
unless its code and data path are trusted, and never include a secret in the JSON
result. Use an external OS/container sandbox and a dedicated account if the
plugin needs a stronger boundary.
