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
  refused on the local control socket and served only over the dashboard — and
  they require the dashboard token **even in local-trust mode** (see below), so an
  agent on the same loopback cannot change its own policy or add a server either by
  shelling out to `curl` the socket or by hitting the TCP dashboard. The vault is
  never readable via the API.
- **Human approval routes need a token on *both* transports.** The approval
  actions (`/api/{approve,deny,stop_all}`) must stay reachable for the human
  (`termada approve|deny|stop` over the socket; the dashboard over TCP), so they
  can't be refused outright. Instead: over the **socket** they require the CLI auth
  token (`~/.config/termada/cli.token`, mode `0600`) which only the CLI sends; over
  **TCP** they require the dashboard token *even in local-trust mode*, because a
  malicious agent runs on the same loopback/uid and `local = trusted` does not hold
  for it. A tokenless `curl` on either transport is refused, so an agent cannot
  self-approve a command it parked under a `confirm` policy. See the limits below
  for the residual same-uid caveat.

## What IS protected

| Guarantee | Notes |
|-----------|-------|
| Vault secrets are never returned to an agent or shown in the dashboard/audit | Only secrets *managed by the vault*. |
| `file_read`/`file_write` refuse protected secret paths | The daemon runtime dir (`cli.token`, dashboard token, vault, audit log, registry, `known_hosts`), the vault file, and `~/.ssh` / `~/.aws` / `~/.gnupg`; canonicalized so `../` and symlinked parents can't slip past (C2/FS-3). Extend via `security.protected_paths`. Does **not** cover `exec` (see limits). |
| Agent sessions can run under a separate uid (opt-in) | `security.run_as` (daemon as root) drops local shells to an unprivileged user, so an agent's `exec` can't read the socket, tokens, vault or host credentials (SEC-8). Off by default; fails closed if set without root. |
| Secret input (sudo/SSH passwords, `exec_write secret=true`) is not logged or echoed | Excluded from audit and replay. |
| Credentials encrypted at rest | age (CGO-free) or OS keychain. |
| Tamper-**evident** audit | Hash-chained; any edit/deletion breaks the chain (`termada audit verify`). |
| Dangerous commands gated by human approval | Policy `confirm`/`deny`; deny-by-default on timeout; agents can't self-approve — approval routes require a token on both transports (CLI token on the socket, dashboard token on TCP even in local-trust); see limits re: same-uid. |
| Dashboard API gated | Token (≥128-bit) on `/api/*` and `/metrics`; loopback Host/Origin checks (anti-DNS-rebinding). In local-trust mode read/observe routes answer tokenless, but the security-sensitive mutating routes (approve/deny/stop_all, policy/server management) require the token **even then** (an agent shares the loopback). Static assets serve freely on loopback (no secrets in them). |

## What is NOT protected (know the limits)

- **Secrets on the host filesystem / environment — unless you enable uid
  separation.** The `file_read`/`file_write` API refuses the protected secret
  paths above, so an agent can't exfiltrate `cli.token`, the vault, `~/.ssh`, etc.
  *through the file tools*. By **default**, though, an agent's `exec` runs a shell
  as the **same uid** as the daemon, so `cat ~/.ssh/id_rsa` (or
  `cat ~/.config/termada/cli.token`) still works — the path-deny covers the file
  API, not the shell. **Set `security.run_as`** (a dedicated unprivileged user;
  the daemon must run as root) to drop agent sessions to that uid: their `exec`
  then can't read the `0600` socket, `cli.token`, the vault, or the operator's
  `~/.ssh`/`~/.aws`. Trade-offs: the agent uid needs group/ACL access to the
  working directories it edits, and the daemon's **environment** is still
  inherited by the shell, so run the daemon with a clean environment (don't export
  host secrets into it).
- **The local control socket is local-trust, and by default agents share the
  uid.** The approval routes (approve/deny/stop-all) are gated by the CLI auth
  token on the socket and the dashboard token on TCP (even in local-trust), so a
  *blind* agent `curl` on either transport is refused, and the `file_read` path to
  the token is closed (it's a protected path). The one residual is `exec`: with
  the default same-uid sessions an agent can `cat ~/.config/termada/cli.token`
  (or the dashboard token) from its shell and forge the header. So the tokens are
  defense-in-depth by default — self-approval is no longer a one-line tokenless
  `curl`, it takes a deliberate, auditable `cat` from a shell — and become a
  **hard** boundary once `security.run_as` is set (the dropped uid can neither open
  the `0600` socket nor read the token files). (Routes that aren't self-escalating
  — e.g. `vault/unlock`, which needs a passphrase the agent doesn't hold and never
  returns secrets — remain tokenless on the socket.)
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
