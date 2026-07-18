# Demo Prompt

Use GPT-5.6 reasoning and Termada MCP tools for the complete task. Do not use a raw shell.

The isolated demo service is degraded. Restore it safely and leave a verifiable report:

1. Create a mission titled `Restore checkout health` with target `local`, workspace `checkout-demo`, and this plan:
   - Establish the expected failure.
   - Identify the faulty service mode.
   - Apply the minimal fix through the protected operation.
   - Verify the health endpoint is restored.
2. Use the dedicated session returned by `mission_create` for every command.
3. Change the session directory to the workspace path printed by `run.sh`.
4. Run `./probe.py broken`, inspect `service.mode`, then run `./apply-fix.sh`.
5. When Termada requests approval, stop and ask me to approve it in Mission Control. Do not bypass the policy.
6. After approval, run `./probe.py healthy`.
7. Update each plan step with the real successful `job_id` that proves it, mark the mission succeeded with a concise outcome, and export `mission_report`.

Do not claim success unless the final probe actually exits with code 0.
