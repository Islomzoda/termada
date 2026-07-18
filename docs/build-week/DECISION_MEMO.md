# Product Decision: Termada Mission Control

## User And Problem

The target user is a developer or small platform/SRE team that wants Codex to diagnose and repair real local or remote environments without granting an opaque, unbounded shell. A terminal or a generic MCP shell exposes individual commands, but it does not preserve the operational goal, the reasoning plan, human approvals, verification evidence, and outcome as one durable unit.

## Decision

Build one workflow: **Termada Mission Control**.

GPT-5.6 in Codex turns an operational goal into a testable plan, creates a dedicated Termada mission/session through MCP, executes every command through the existing policy-controlled runtime, stops for human approval, attaches successful runtime job ids to completed plan steps, and exports an evidence report. Termada supplies the deterministic execution, access controls, persistence, audit anchors, and operator UI; the model supplies diagnosis and adaptive planning.

This is stronger than a raw terminal or standalone shell MCP server because the workflow is persistent, owner-scoped, policy-gated, observable by a human, recoverable after daemon interruption, and unable to mark a step verified without a real `exited/0` job from the mission.

## Judge Experience

In the first 30 seconds, judges see a concrete goal, a live plan, an HTTP 503 diagnosis, and a command parked by policy in a visible approval band. In the 2–3 minute demo, the human approves once, GPT-5.6 completes the repair, a real HTTP 200 check passes, and Termada exports a Markdown report with audit sequence/hash anchors and its own SHA-256.

The scope deliberately excludes general workflow automation, fleet approvals, and a credential broker. One polished, reproducible repair mission is achievable before the submission deadline and exercises Termada's real differentiators.

## Honest Baseline

Before Build Week, Termada already had persistent PTY sessions, SSH, policy confirm/deny, approvals, a live dashboard, vault, and a tamper-evident audit. Mission state, plan verification, session-attempt recovery, mission-first UI, evidence reports, and the isolated demo are the meaningful Build Week extension.
