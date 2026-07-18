# Judging Alignment

The [official rules](https://openai.devpost.com/rules) weight four criteria equally at 25%.

## Technological Implementation (25%)

- GPT-5.6 in Codex performs the adaptive operational reasoning: plan, diagnosis, action choice, approval pause, verification, and outcome.
- Termada contributes a non-trivial deterministic runtime: persistent PTY/SSH sessions, policy/approval, durable event ordering, owner isolation, interruption recovery, evidence validation, audit anchors, and report hashing.
- The model cannot fabricate a passed step through the tool contract; Termada validates a real job/session/status/exit code.
- The isolated `verify_demo.sh` exercises MCP stdio, the daemon, HTTP control plane, policy queue, operator approval, health change, and report generation end to end.

## Design And User Experience (25%)

- The first screen is an operational Mission Control surface, not a marketing page.
- Goal, status, target, owner, session, progress, plan, approval, timeline, and outcome have a clear scan order.
- Approval is pinned in the first viewport on desktop and mobile while also remaining in the evidence timeline.
- Offline/stale data is distinguishable from a genuine empty state, stream reconnect has Retry, and controls retain visible focus/touch targets.
- Browser QA covered 1440×900 and 390×844, including pixel screenshots, overflow checks, console logs, and a real approval click.

## Potential Impact (25%)

- Clear users: developers and small platform/SRE teams delegating local or remote operational work to Codex.
- Clear value: faster diagnosis/repair without losing human control, execution context, or a reviewable handoff.
- Local-first open source avoids requiring a new hosted control plane for terminal output and credentials.
- Evidence reports can support incident review, code review, handoffs, and future compliance integrations.

## Quality And Originality (25%)

- Mission Control is not another chat UI or raw shell MCP server. It is an operational trust layer between model reasoning and infrastructure effects.
- It combines model planning with deterministic proof constraints: the LLM proposes and adapts, while the runtime decides what can execute and what counts as verified.
- Honest recovery and explicit agent-supplied versus runtime-observed evidence avoid common agent-demo overclaims.

## Existing Project Before/After

| Before Build Week | Built During Build Week |
| --- | --- |
| PTY/SSH sessions and jobs | Durable mission, plan, status, and session attempts |
| Command policy and approval queue | Mission-correlated/pinned approval experience |
| Global dashboard and activity history | Mission-first plan/evidence dashboard |
| Hash-chained audit | Evidence report with audit anchors and report SHA-256 |
| Job metadata recovery | Interrupted mission + fresh-session resume |
| General runtime tests | Isolated one-command real MCP repair demo |
