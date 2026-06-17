# Termada — security & threat model

Termada gives **honest best-effort** guarantees, not absolutes. This document
states what is and isn't protected so you can decide what to trust it with.
(See spec §3a.)

## Trust boundaries

- **Agents are untrusted.** Everything an agent sends over MCP is data. Commands
  are passed as an **argv array** and quoted before execution, so shell
  metacharacters in arguments are literal, not operators.
- **The human (dashboard/CLI) is trusted.** The dashboard is the human control
  surface: it can approve/deny, kill, take over input, and manage servers. It is
  reachable only on loopback and gated by a token.
- **Policy/server management is dashboard-only, never an MCP tool** (SEC-7). The
  mutating routes (`/api/policies/{set,remove}`, `/api/servers/{add,remove}`) are
  refused on the local control socket and served only over the token-gated
  dashboard — so an agent cannot change its own policy or add a server even by
  shelling out to `curl` the socket. The vault is never readable via the API.
- **Human approval routes require a CLI auth token on the socket.** The approval
  actions (`/api/{approve,deny,stop_all}`) must stay reachable on the socket
  because the human CLI (`termada approve|deny|stop`) uses them, so they can't be
  refused outright like the routes above. Instead they require the CLI auth token
  (`~/.config/termada/cli.token`, mode `0600`), which the CLI reads and sends; a
  tokenless `curl` from an agent is refused. This stops an agent from
  self-approving a command it parked under a `confirm` policy. See the limits
  below for the residual same-uid caveat.

## What IS protected

| Guarantee | Notes |
|-----------|-------|
| Vault secrets are never returned to an agent or shown in the dashboard/audit | Only secrets *managed by the vault*. |
| `file_read`/`file_write` refuse protected secret paths | The daemon runtime dir (`cli.token`, dashboard token, vault, audit log, registry, `known_hosts`), the vault file, and `~/.ssh` / `~/.aws` / `~/.gnupg`; canonicalized so `../` and symlinked parents can't slip past (C2/FS-3). Extend via `security.protected_paths`. Does **not** cover `exec` (see limits). |
| Secret input (sudo/SSH passwords, `exec_write secret=true`) is not logged or echoed | Excluded from audit and replay. |
| Credentials encrypted at rest | age (CGO-free) or OS keychain. |
| Tamper-**evident** audit | Hash-chained; any edit/deletion breaks the chain (`termada audit verify`). |
| Dangerous commands gated by human approval | Policy `confirm`/`deny`; deny-by-default on timeout; agents can't self-approve (socket approval routes require the CLI auth token; see limits re: same-uid). |
| Dashboard API gated | Token (≥128-bit) on `/api/*` and `/metrics`; loopback Host/Origin checks (anti-DNS-rebinding). Static assets serve freely on loopback (no secrets in them). |

## What is NOT protected (know the limits)

- **Secrets on the host filesystem / environment.** The `file_read`/`file_write`
  API refuses the protected secret paths above, so an agent can't exfiltrate
  `cli.token`, the vault, `~/.ssh`, etc. *through the file tools*. But an agent
  with `exec` runs a shell as the **same uid** as the daemon, so `cat ~/.ssh/id_rsa`
  (or `cat ~/.config/termada/cli.token`) still works — the path-deny covers the
  file API, not the shell. Closing the `exec` vector needs sessions under a
  restricted uid (not yet automated; see below).
- **The local control socket is local-trust, and agents run as the same uid.**
  The approval routes (approve/deny/stop-all) are now gated by the CLI auth token
  above, so a *blind* agent `curl` of the socket is refused. The `file_read` path
  to the token is also closed now (it's a protected path). But agent sessions
  currently run as the **same uid** that owns the socket and the `0600`
  `cli.token` (no uid separation yet — see below), so an agent with `exec` can
  still `cat` that token file from its shell and forge the header. The token
  therefore raises the bar and is defense-in-depth today — a self-approve is no
  longer a one-line tokenless `curl`; it requires deliberately `cat`-ing the CLI
  token from a shell, a louder and auditable act. The **complete** guarantee
  needs the agent's sessions under a restricted uid that can neither open the
  `0600` socket nor read `cli.token` — at which point the token gate becomes a
  hard boundary. That uid separation is the same mitigation noted above and is
  not yet automated. (Routes that are not self-escalating — e.g. `vault/unlock`,
  which needs a passphrase the agent doesn't hold and never returns secrets to an
  agent — remain tokenless on the socket.)
- **Output redaction is best-effort.** Known token formats (PEM/JWT/AWS/GCP/
  api-key/GitHub/Slack) plus exact vault values are masked; an arbitrary secret
  may slip through.
- **Tamper-evidence is not tamper-proof.** A local root who can rewrite the whole
  audit file from the head defeats it. True resistance needs the chain head
  anchored outside the file (keychain/TPM/remote) — a later phase.
- **At-runtime secrets.** While the vault is unlocked, keys live in the daemon's
  memory and are not protected against a local root with ptrace.
- **`agent_id` is currently self-asserted** (from MCP `clientInfo`). For a hostile
  multi-agent setup, bind identity to a per-agent token (planned).

## Reporting

This is a 0.x project; a formal security review is pending (RL-3). Please report
issues via the GitHub repository.
