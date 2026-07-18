# Mission Control Demo

This is an isolated, real execution path for the Build Week demonstration. It starts:

- a broken loopback HTTP service on an ephemeral port;
- a separate Termada home and daemon on an ephemeral dashboard port;
- a `build-week` agent policy that requires human approval for `apply-fix.sh`.

No external server, account, container, credential, or network access is used. The protected operation really changes `service.mode` and sends `SIGHUP` to the demo service; the health probes make real HTTP requests.

```bash
./demo/mission-control/run.sh
```

Register the printed `mcp.sh` command in a Codex MCP configuration, then give Codex the contents of [PROMPT.md](PROMPT.md). The full path is designed for a 2–3 minute recording.

To use an already installed candidate binary instead of building the checkout:

```bash
TERMADA_BIN="$HOME/.local/bin/termada" ./demo/mission-control/run.sh
```

Stop only the isolated demo processes with:

```bash
./demo/mission-control/stop.sh
```

For CI or release verification, `verify_flow.py` drives the same operations over the real MCP stdio transport and resolves the parked command through the authenticated operator API:

```bash
./demo/mission-control/verify_flow.py
```

This verifier is not the video path; the recorded demo uses a visible human click in Mission Control.
For a clean one-command release check that starts and stops its own isolated environment, run `./demo/mission-control/verify_demo.sh`.
