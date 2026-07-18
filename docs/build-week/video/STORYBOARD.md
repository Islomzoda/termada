---
format: 1920x1080
message: "AI agents can operate real environments without losing human control or verifiable evidence"
arc: Hook -> Existing foundation -> Diagnosis -> Approval -> Proof -> Evidence and CTA
audience: OpenAI Build Week judges, developers, and small platform teams
mode: autonomous
music: none
captions: skipped (clean product UI and concise on-screen callouts)
---

## Frame 1 — The trust gap

- status: outline
- src: compositions/frames/01-trust-gap.html
- duration: 16s
- transition_in: cut
- scene: Termada name and the gap between a successful command and a trustworthy outcome.
- voiceover: AI agents can run shell commands, but a successful command is not the same as a trustworthy outcome.
- poster: 6s
- asset_candidates: assets/mission-complete-desktop.jpg

Open on the actual product behind a restrained dark focus layer. The product
name and one-line promise enter first; three proof words follow: policy,
approval, evidence.

## Frame 2 — What Build Week added

- status: outline
- src: compositions/frames/02-build-week-delta.html
- duration: 24s
- transition_in: push
- scene: Before-versus-now system boundary without diminishing the existing runtime.
- voiceover: Before Build Week Termada had the runtime. During Build Week we added the durable mission and evidence layer.
- poster: 9s
- asset_candidates: assets/approval-desktop.jpg

Use a split operational ledger: existing PTY, SSH, policy, vault, and audit on
the left; mission plan, recovery, verified steps, and report on the right.

## Frame 3 — A real 503 diagnosis

- status: outline
- src: compositions/frames/03-diagnosis.html
- duration: 30s
- transition_in: push
- scene: The real approval-state screenshot with the first two runtime-verified steps emphasized.
- voiceover: GPT-5.6 creates a four-step mission. Real jobs reproduce HTTP 503 and identify the broken service mode.
- poster: 15s
- asset_candidates: assets/approval-desktop.jpg

Pan across the real mission UI from goal to plan. Overlay only exact facts:
HTTP 503, `service.mode = broken`, and 2/4 verified.

## Frame 4 — Human approval is the boundary

- status: outline
- src: compositions/frames/04-approval.html
- duration: 30s
- transition_in: focus
- scene: Approval band enlarged with exact command, matching rule, and Allow once.
- voiceover: The protected repair stops at policy. Nothing continues until a human explicitly allows it once.
- poster: 14s
- asset_candidates: assets/approval-desktop.jpg

Keep the approval screenshot legible. The amber rule and exact command are the
primary focal point; deterministic callouts identify policy and human control.

## Frame 5 — Verified outcome

- status: outline
- src: compositions/frames/05-verified.html
- duration: 24s
- transition_in: push
- scene: Completed real mission at 4/4 with HTTP 200 and Succeeded.
- voiceover: The same session applies the fix, verifies HTTP 200, and reaches four of four runtime-backed steps.
- poster: 12s
- asset_candidates: assets/mission-complete-desktop.jpg

Reveal the green success state, then guide the eye through all four verified
steps and the recorded outcome.

## Frame 6 — Evidence, roles, and CTA

- status: outline
- src: compositions/frames/06-evidence.html
- duration: 17s
- transition_in: focus
- scene: Evidence report anchors, model/runtime role split, and open-source CTA.
- voiceover: GPT-5.6 supplies adaptive planning. Termada supplies deterministic execution and proof. Let the agent move fast without losing control.
- poster: 9s
- asset_candidates: assets/evidence-report.md, assets/mission-complete-desktop.jpg

Show the real mission id and report hash, then finish on the product name,
open-source availability, macOS and Linux, and the control-first promise.

## Video direction

Medium-energy operational product proof. Real v0.11.0 screens remain dominant.
Use Space Grotesk for claims and IBM Plex Mono for commands, HTTP states, ids,
and hashes. Alternate horizontal push and focus transitions. All motion is
seekable and deterministic; no music, fake terminal output, or decorative
effects that compete with the evidence.
