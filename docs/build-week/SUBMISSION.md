# Devpost Submission

## Project

**Title:** Termada Mission Control

**Tagline:** Let Codex operate real environments through persistent, policy-gated missions with human approvals and verifiable evidence.

**Track:** Developer Tools

## Opening Hook

What if Codex could repair a production-like service without receiving an invisible, unbounded shell, and every claim of success had to point to execution evidence?

## The Problem

AI agents are increasingly capable of diagnosing and changing real systems, but today's shell access is fragmented and hard to trust. A command may hang on input, disappear after a disconnect, bypass the operator's attention, or end with an unsupported “fixed” claim. Generic shell MCP servers expose commands; they do not preserve the goal, plan, policy decisions, human approvals, verification jobs, and outcome as one reviewable operation.

## The Solution

Termada Mission Control turns an operational task into a durable mission. GPT-5.6 in Codex creates a testable plan and a dedicated Termada session through MCP. Every command then runs through Termada's real PTY/SSH runtime. The dashboard shows the mission, plan progress, command lifecycle, policy decisions, and operator attention in real time. A confirm-matched action stops until a human approves it. A plan step cannot be marked passed unless it references a job from that mission that Termada observed exiting with code zero. The final Markdown evidence report contains the goal, agent-supplied outcome, runtime-observed events, exact audit chain anchors, and a SHA-256 recorded back into the audit.

If the daemon restarts, an unfinished mission becomes `interrupted`. Codex can resume it in a fresh session attempt while preserving prior evidence and being warned to re-check remote state.

## What We Built

- A bounded, owner-scoped, atomically persisted mission model with goals, plans, status transitions, multiple session attempts, and recovery.
- Six MCP tools: `mission_create`, `mission_list`, `mission_get`, `mission_update`, `mission_resume`, and `mission_report`.
- Runtime correlation by dedicated session id, including command starts/finishes, approval requests/resolutions, operator input, reconnects, and policy denial.
- Evidence enforcement: `passed` steps require an actual `job_id` from the mission with `status=exited` and `exit_code=0`; success is blocked while any mission job is active.
- A mission-first desktop/mobile dashboard with plan progress, a compact evidence timeline, pinned approval controls, reconnect/stale states, keyboard-safe navigation, and report download.
- A deterministic Markdown report with audit record sequence/hash anchors and a report SHA-256.
- A no-secret local demo with a real broken HTTP service, a real 503-to-200 repair, a real policy approval, and an automated MCP verification path.

## How We Built It

Termada remains a dependency-light Go single binary. A new `internal/mission` service sits above the existing engine rather than duplicating execution. `mission_create` allocates a normal persistent session, so commands continue through the proven engine, policy, approval, audit, redaction, and SSH paths. The daemon delivers events to one ordered durable sink: the hash-chained audit first, then the mission's bounded redacted projection. The control plane exposes owner-scoped JSON routes; the MCP stdio shim proxies them to Codex. The embedded dashboard uses dependency-free HTML/CSS/JavaScript and SSE refreshes.

We used Codex as the primary engineering environment for architecture analysis, implementation, tests, demo automation, browser QA, documentation, and iteration. GPT-5.6 is also part of the product workflow: it converts a human goal into the mission plan, chooses diagnostic actions, reacts to results, pauses at approval, and attaches real job evidence before declaring success. It does not replace Termada's deterministic policy or evidence checks.

## Challenges

- Preserving a single source of operational truth while adding mission state. Audit writes remain authoritative and ordered before mission projections.
- Preventing “AI said it passed” from becoming evidence. The mission service validates job ownership, session attempt, terminal status, and exit code.
- Recovering honestly. PTY sessions do not survive a daemon restart, so resume creates a fresh attempt instead of pretending shell state persisted.
- Keeping an operational dashboard dense but readable on both 1440×900 and 390×844. Browser QA caught and fixed global CSS collisions and an approval action that initially appeared below the fold.

## Potential Impact

Mission Control gives individual developers and small operations teams a practical trust layer between an agent and real infrastructure. It can reduce the time spent reconstructing what an agent did, make risky changes explicitly human-controlled, and produce evidence that is useful for code review, incidents, handoffs, and regulated workflows. The architecture is local-first and open source, so teams can adopt it without sending terminal output or credentials to a new hosted control plane.

## What Is Next

- Build evidence reports directly from retained audit queries at larger scale and optionally anchor report hashes outside the host.
- Add interactive approval flows for fleet, plugin, and port-forward operations, which currently fail closed on `confirm`.
- Add metadata-only policy/audit coverage for file tools without storing file content.
- Add native Windows ConPTY execution and signed/notarized release artifacts.
- Add optional action-bound credential leases without exposing plaintext credentials to the model.

## Technologies

Go 1.26, Model Context Protocol over JSON-RPC/stdio, GPT-5.6 in Codex, PTY, SSH/SFTP, Server-Sent Events, age encryption, SHA-256 hash chains, embedded HTML/CSS/JavaScript, Python standard library for the isolated demo.

## Existing Project Disclosure

Termada is an existing Apache-2.0 open-source project. Before the OpenAI Build Week submission period it already provided the terminal runtime, daemon, persistent sessions, SSH, policies, approvals, vault, audit, and dashboard. The judged extension is Mission Control: the mission model and MCP contracts, evidence validation/reporting, mission-first dashboard, interruption/resume workflow, isolated demo, tests, and submission materials. The change is visible in the dated commit range prepared after July 13, 2026 at 09:00 PT and in the primary Codex task used for `/feedback`.

## Install And Test

Supported production platforms are macOS and Linux on `amd64` and `arm64`. Windows archives cross-compile, but native PTY execution awaits ConPTY.

```bash
curl -fsSL https://raw.githubusercontent.com/Islomzoda/termada/main/install.sh | sh
./demo/mission-control/run.sh
```

For the source checkout/release candidate:

```bash
./demo/mission-control/verify_demo.sh
```

The demo needs only Go for a checkout build and Python 3 for the isolated service. It uses loopback ephemeral ports and no external account, secret, container, or server.

## Limits

Termada is not a sandbox. Local commands retain the OS permissions of their configured execution uid, redaction is best-effort, remote kill is best-effort Ctrl-C, and the local audit chain is tamper-evident rather than tamper-resistant without an external anchor. File tools do not yet share the complete command policy/audit path. The demo intentionally uses `exec_*`, where policy, approval, and audit are enforced.

## Final Call To Action

Give Codex a real operational goal, not an invisible shell. Watch the mission, control the risky moment, and keep the evidence.

Official event references: [OpenAI Build Week](https://openai.com/build-week/), [Official Rules](https://openai.devpost.com/rules), [FAQ](https://openai.devpost.com/details/faqs), and [Schedule](https://openai.devpost.com/details/dates).
