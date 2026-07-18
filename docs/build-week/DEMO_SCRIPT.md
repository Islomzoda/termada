# Demo Script (2:40)

## Setup Before Recording

Run `./demo/mission-control/run.sh`, register the printed `mcp.sh` command in Codex, open the printed dashboard URL, and place Codex and Mission Control side by side. Use the exact prompt in `demo/mission-control/PROMPT.md`. Do not show the token, local config directory, vault, unrelated audit records, or real servers.

## Timeline

**0:00–0:12 — Hook**

Voiceover: “Codex can fix real systems, but a raw shell gives us commands without a trustworthy operation. Termada turns the whole job into a policy-controlled mission with evidence.”

Show the broken service goal in Codex and the empty Mission Control view.

**0:12–0:30 — GPT-5.6 creates the mission**

Codex calls `mission_create` with four testable steps. The dashboard immediately shows the goal, local target, dedicated session, and 0/4 plan.

Voiceover: “GPT-5.6 plans and adapts; Termada owns execution, policy, persistence, and proof.”

**0:30–0:58 — Real diagnosis**

Codex changes the persistent session to the isolated workspace, runs `./probe.py broken`, and reads `service.mode`. Show the real HTTP 503/degraded result and the first two steps becoming runtime-verified with job ids.

**0:58–1:28 — Human approval**

Codex runs `./apply-fix.sh`. Termada returns `awaiting_confirmation`. Pause on the dashboard approval band showing the exact command and matching rule.

Voiceover: “The model cannot approve itself or disguise the action. The runtime parks it and denies by default on timeout.”

Click **Allow once**. Show the approval band disappear and the command continue.

**1:28–1:55 — Verification**

Codex runs `./probe.py healthy`. Show the real HTTP 200/status ok output and the plan reaching 4/4.

Voiceover: “A step only passes when it references a job from this mission that Termada observed exiting with code zero.”

**1:55–2:22 — Evidence report**

Codex marks the mission succeeded and calls `mission_report`. Click the download icon. Show the report sections: goal, outcome, verified steps, runtime events, audit sequence/hash anchors, integrity limits, and report SHA-256.

**2:22–2:35 — Interruption story**

Briefly show the architecture diagram or an `interrupted` screenshot.

Voiceover: “If the daemon restarts, Termada does not pretend the shell survived. Codex resumes in a fresh session attempt and re-verifies state.”

**2:35–2:40 — Close**

Voiceover: “Termada Mission Control: give Codex a goal, control the risky moment, and keep the evidence.”

On screen: repository URL and `Apache-2.0`.
