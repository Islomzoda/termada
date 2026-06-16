# Termada plugins

Plugins let you add custom MCP tools without modifying termada. A plugin is just
an **executable** dropped into `~/.config/termada/plugins/`. It runs
**out-of-process** with a minimal environment — no access to the vault, the audit
key, or the dashboard token (spec §29 capability model). Its tools appear to
agents namespaced as `<plugin>.<tool>`, and every call still passes through
policy, audit and redaction like any other command.

## Protocol

The daemon invokes your executable two ways:

```
<plugin> describe
  → stdout: {"tools":[{"name":"...","description":"...","inputSchema":{...}}]}

<plugin> call <tool>
  ← stdin:  the tool arguments as JSON
  → stdout: the result as JSON
```

- `describe` is called once at load (5s budget) to enumerate tools.
- `call <tool>` runs the tool; arguments arrive as JSON on stdin, the result is
  JSON on stdout (60s budget).
- A non-zero exit or non-JSON output is surfaced to the agent as a tool error.

## Example (bash)

```bash
#!/usr/bin/env bash
# ~/.config/termada/plugins/greet  (chmod +x)
case "$1" in
  describe)
    echo '{"tools":[{"name":"hello","description":"Greet someone","inputSchema":{"type":"object","properties":{"who":{"type":"string"}}}}]}'
    ;;
  call)
    who=$(python3 -c 'import sys,json;print(json.load(sys.stdin).get("who","world"))')
    echo "{\"greeting\":\"hello $who\"}"
    ;;
esac
```

The agent then sees a tool `greet.hello` and calls it like any other.

## Safety

- Plugins run with a stripped environment and no termada secrets.
- Tool calls are subject to the same policy/audit as built-in tools.
- Only executables in the configured plugins directory are loaded; load happens
  at daemon start.
