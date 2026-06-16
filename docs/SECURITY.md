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
- **Credential/server management is CLI- and dashboard-only, never an MCP tool**
  (SEC-7). An agent cannot change its own policy, add servers, or read the vault.

## What IS protected

| Guarantee | Notes |
|-----------|-------|
| Vault secrets are never returned to an agent or shown in the dashboard/audit | Only secrets *managed by the vault*. |
| Secret input (sudo/SSH passwords, `exec_write secret=true`) is not logged or echoed | Excluded from audit and replay. |
| Credentials encrypted at rest | age (CGO-free) or OS keychain. |
| Tamper-**evident** audit | Hash-chained; any edit/deletion breaks the chain (`termada audit verify`). |
| Dangerous commands gated by human approval | Policy `confirm`/`deny`; deny-by-default on timeout; agents can't self-approve. |
| Dashboard API gated | Token (≥128-bit) on `/api/*` and `/metrics`; loopback Host/Origin checks (anti-DNS-rebinding). Static assets serve freely on loopback (no secrets in them). |

## What is NOT protected (know the limits)

- **Secrets on the host filesystem / environment.** An agent with `exec`/
  `file_read` can read `~/.ssh`, `~/.aws/credentials`, env, etc. The vault
  boundary does not cover these. Mitigation: run sessions under a restricted uid
  (not yet automated).
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
