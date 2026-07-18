# Build Week Release Checklist

Submission closes **July 21, 2026 at 5:00 PM PDT** (**July 22 at 5:00 AM Asia/Dushanbe**). The deadline is consistent across the [official rules](https://openai.devpost.com/rules) and [Devpost schedule](https://openai.devpost.com/details/dates).

## Code And Evidence

- [ ] Review the final diff and commit the Build Week extension as a clearly dated commit range after July 13, 2026 at 09:00 PT.
- [ ] Run the full preflight listed below from a clean checkout.
- [ ] Run `./demo/mission-control/verify_demo.sh` and retain its mission id/report SHA-256 in release notes.
- [ ] Confirm `git diff --check` and a secret scan are clean.
- [ ] Confirm no `.codex/`, temp demo state, token, vault, audit log, or real server data is staged.
- [ ] Run `/feedback` in the primary Codex task where the core implementation was built; save the returned Session ID in Devpost.

## Release (Requires Owner Approval)

- [ ] Choose the next version and align `cmd/termada/main.go`, `server.json`, `.claude-plugin/plugin.json`, and `CHANGELOG.md`.
- [ ] Create the release commit/tag only after explicit approval.
- [ ] Push the branch/tag only after explicit approval.
- [ ] Verify GitHub release assets/checksums, Homebrew, GHCR, and MCP Registry propagation.
- [ ] Install the released binary into a fresh demo home and rerun the no-rebuild test path.
- [ ] Do not restart the user's normal daemon if `termada status` shows active jobs.

## Video

- [ ] Public YouTube URL, maximum 3:00, with audible English voiceover.
- [ ] Show the real product and real execution, not slides alone.
- [ ] State what existed before Build Week and what was newly built.
- [ ] Explain the distinct roles of Codex, GPT-5.6, and Termada.
- [ ] Show mission creation, real 503, approval, real 200, and evidence report.
- [ ] Remove tokens, personal paths, real credentials, and unrelated tabs from the recording.
- [ ] Use only licensed/original music and visual assets.

## Devpost

- [ ] Select **Developer Tools**.
- [ ] Paste and proofread [SUBMISSION.md](SUBMISSION.md).
- [ ] Add repository URL and confirm Apache-2.0 visibility.
- [ ] Add installation instructions, supported platforms, demo command, and free judging access.
- [ ] Add the public YouTube URL and primary `/feedback` Session ID.
- [ ] Upload lead/evidence screenshots from [SCREENSHOTS.md](SCREENSHOTS.md).
- [ ] Verify every link in a logged-out browser.
- [ ] Submit before the deadline; Devpost does not allow edits after the submission period.
- [ ] Keep the demo/repository accessible through winner announcement on August 12 because official pages disagree on the exact judging end date.

## Full Preflight

```bash
go mod verify
sh -n install.sh demo/mission-control/*.sh
jq empty server.json .claude-plugin/plugin.json
node --check internal/dashboard/assets/app.js
node --check internal/dashboard/assets/mission.js
go vet ./...
go test -race -count=1 -p 1 ./...
go build -o /tmp/termada-build-week ./cmd/termada
/tmp/termada-build-week version
./demo/mission-control/verify_demo.sh
mcp-publisher validate server.json
git diff --check
```
