# Mission Control Architecture

Termada Mission Control is an orchestration/evidence layer above the existing execution engine. It does not introduce a second shell path.

```mermaid
sequenceDiagram
    participant User
    participant GPT as GPT-5.6 in Codex
    participant MCP as Termada MCP shim
    participant Mission as Mission service
    participant Engine as PTY/SSH engine
    participant Policy as Policy + approval
    participant Audit as Hash-chained audit
    participant UI as Mission Control

    User->>GPT: Restore the degraded service
    GPT->>MCP: mission_create(goal, plan)
    MCP->>Mission: Create durable mission
    Mission->>Engine: Create dedicated session
    GPT->>MCP: exec_run(session, diagnostic)
    MCP->>Engine: Start command
    Engine->>Policy: Evaluate argv
    Policy-->>Engine: allow / deny / confirm
    Engine->>Audit: Durable runtime event
    Audit-->>Mission: Ordered event projection
    Mission-->>UI: Goal, plan, timeline via API/SSE
    Policy-->>UI: Approval required
    User->>UI: Allow once
    UI->>Engine: Authenticated operator approval
    GPT->>Mission: mission_update(step, successful job_id)
    Mission->>Engine: Validate session + exited/0
    GPT->>Mission: mission_report
    Mission->>Audit: Read session-attempt anchors
    Mission-->>GPT: Markdown + SHA-256
```

## Components

- `internal/mission`: bounded model, atomic JSON store, owner isolation, transitions, interruption/resume, runtime projection, Markdown reporting.
- `internal/engine`: unchanged authority for sessions, jobs, PTY/SSH, policy evaluation, quotas, input, signals, and lifecycle.
- `internal/audit`: authoritative ordered durable record with fsync, rotation, sequence, and hash chain.
- `internal/controlplane`: owner-scoped mission routes plus authenticated operator report/download and approval access.
- `internal/mcp`: six mission tools and evidence-bearing `job_id` in every `exec_run` result.
- `internal/dashboard`: compact mission list, plan, timeline, pinned approval, reconnect/stale states, and report download.

## Data And Recovery

The mode-`0600` mission store keeps at most 128 missions, 24 steps per mission, and 512 redacted runtime events. Terminal output is not copied into it. A mission records all session attempt ids; after a daemon restart, any non-terminal mission becomes `interrupted`. `mission_resume` creates a fresh shell and preserves prior evidence.

Audit is written before the mission projection in one ordered reliable sink. If either durable write fails, new execution fails closed. Reports query up to the bounded retained audit tail, include matching record sequence/hash anchors, exclude prior report-generation records for deterministic output, then record the report SHA-256 as `mission.report_generated`.

## Trust Boundary

The model can create, read, update, resume, and report only its owner-scoped missions. It cannot call approve/deny. The dashboard or human CLI authenticates separately as an operator. `passed` is not an assertion-only field: the service resolves the referenced job, checks it belongs to a session attempt for that mission, and requires `exited/0`.

See [SECURITY.md](../SECURITY.md) for the complete threat model and limitations.
