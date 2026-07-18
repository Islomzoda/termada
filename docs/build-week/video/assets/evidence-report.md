# Evidence Report: Restore checkout health

- Mission: `msn_d864e6ebf58a41c6`
- Status: **succeeded**
- Target: `local`
- Current session: `sess_5000138bbb64178b`
- Started: 2026-07-18T18:16:00Z
- Completed: 2026-07-18T18:16:26Z
- Agent: `build-week`

## Goal

Restore the isolated checkout service and prove its health endpoint is ready.

## Outcome

Changed the isolated service mode from broken to healthy, reloaded it after human approval, and verified HTTP 200 with status ok.

## Plan And Verified Steps

- [x] **step_1**: Establish the expected failure (`job_e88456dbe064edf8`) - passed. Agent note: HTTP 503 and degraded status reproduced
- [x] **step_2**: Identify the faulty service mode (`job_45d55e2ad6021017`) - passed. Agent note: service.mode was broken
- [x] **step_3**: Apply the protected fix (`job_d15e431e451efbf1`) - passed. Agent note: Human-approved SIGHUP reload completed
- [x] **step_4**: Verify the restored health endpoint (`job_31bf690f055cef9a`) - passed. Agent note: HTTP 200 and status ok observed

## Runtime Evidence

| Time (UTC) | Event | Command / detail | Result |
|---|---|---|---|
| 18:16:00 | `job.started` | cd /tmp/termada-demo-v0110-final/broken-checkout |  |
| 18:16:00 | `job.finished` | exited | exited |
| 18:16:00 | `job.started` | ./probe.py broken |  |
| 18:16:01 | `mission.step_updated` | Establish the expected failure | passed |
| 18:16:01 | `job.finished` | exited | exited |
| 18:16:01 | `job.started` | cat service.mode |  |
| 18:16:01 | `mission.step_updated` | Identify the faulty service mode | passed |
| 18:16:01 | `job.finished` | exited | exited |
| 18:16:01 | `confirm.requested` | ./apply-fix.sh |  |
| 18:16:25 | `confirm.resolved` |  | approved by dashboard |
| 18:16:25 | `job.started` | ./apply-fix.sh |  |
| 18:16:26 | `job.finished` | exited | exited |
| 18:16:26 | `mission.step_updated` | Apply the protected fix | passed |
| 18:16:26 | `job.started` | ./probe.py healthy |  |
| 18:16:26 | `mission.step_updated` | Verify the restored health endpoint | passed |
| 18:16:26 | `job.finished` | exited | exited |
| 18:16:26 | `mission.completed` | Changed the isolated service mode from broken to healthy, reloaded it after human approval, and verified HTTP 200 with status ok. | succeeded |

## Audit Chain Anchors

| Seq | Event | Job | Record hash |
|---:|---|---|---|
| 2 | `session.created` | `` | `00ba671589a219e192e1bedf3081f4a0614d7896ef6acbc5e2520e8d3ccaf7e0` |
| 3 | `mission.created` | `` | `1a358640205d505d78db99d87249582191027535774c6fbbdbec50a07cc4bc78` |
| 4 | `job.start_requested` | `` | `4e8b8074257652f062f93bbcd0e296b9b6300d67a9a381977c61219f1f51f442` |
| 5 | `job.started` | `job_55649571dc5efc5a` | `e077a680fd916b339677c26b9e80eb5aac0d70f066a0659d7514c785826c7f17` |
| 6 | `job.finished` | `job_55649571dc5efc5a` | `cc74cd2123c2ada51e5e0ab3c4cb69a400a1717c1a5121557df1703a00b9ad19` |
| 7 | `job.start_requested` | `` | `5209c9f2c58ec0a67a992c715fb29a7846b555a9046e701819d7c4b8295e7779` |
| 8 | `job.started` | `job_e88456dbe064edf8` | `a86c2fcf1d0714d6b5003cc5a2e534f97119f4fcf080375954547ba37ad7fc03` |
| 9 | `job.finished` | `job_e88456dbe064edf8` | `d023df0b399aa311fb99d36cdde1b6e2d2253ceb66e750eb464e98ccaeeb8721` |
| 10 | `mission.updated` | `` | `680ceb0b0eba2bbb5657c8a7168749bd8230d00ae4371a156f0f773325fdcd98` |
| 11 | `job.start_requested` | `` | `33ee567d4fa3cb982e6a5ca673f5797f5fb8d64975ebde2532421b4e4ed6ca19` |
| 12 | `job.started` | `job_45d55e2ad6021017` | `7da5c26b2dd43c75580069bbd04324a6ab33e892eca5b24f1fa11946212d57b7` |
| 13 | `job.finished` | `job_45d55e2ad6021017` | `9a8d80ff6fe714c1e7b4df6224edcffa5df1791f0e46eeeb0e99c03047a40acb` |
| 14 | `mission.updated` | `` | `1956478f7c938a82a279fe3c706ae468158a16908c821e43cef418c56c7efecb` |
| 15 | `confirm.requested` | `job_d15e431e451efbf1` | `3908d92b00c321e5b96629ad6a2c230b08d01ccac51379be06f02bd738b13e58` |
| 16 | `confirm.resolved` | `job_d15e431e451efbf1` | `6a3c15e0eb6904c39887956efe2a63b522b86435016496d83dc582153b626252` |
| 17 | `job.started` | `job_d15e431e451efbf1` | `1e69e5f9de60a5d9e21d18923b062f34c2368d06f6d480a03008710b2b04b2db` |
| 18 | `job.finished` | `job_d15e431e451efbf1` | `ac5a5371cb8a027a38e903614a7f08452d85ea748739c40152448deffdb8ae4d` |
| 19 | `mission.updated` | `` | `8fe849572c74bb241b49502c3556a0f9f91a469a563a576dcfde3767d5a66364` |
| 20 | `job.start_requested` | `` | `21fba8a0839cfc6a001f90d3a6b29be7773f385d9f0dd02c09715a1e6199c41f` |
| 21 | `job.started` | `job_31bf690f055cef9a` | `08075ac6cd855185909d53b6e3296f6d281ad9df1c9585d1af024623e11f7157` |
| 22 | `job.finished` | `job_31bf690f055cef9a` | `7756523e42577fc34167fdf219f7c7f3074c0d65aa6094147fce94cd3b9277bc` |
| 23 | `mission.updated` | `` | `69e33cdef95847357664dee099b74b3c875b737b045859c86ff1ab3a07149e86` |
| 24 | `mission.updated` | `` | `bdca502842f59588b23a511d07cdf8e23f722665792efebee17e295a386766f4` |

## Integrity And Limits

Mission evidence is correlated from Termada runtime events and persisted locally with mode `0600`. Steps marked `passed` reference a job from this mission session that Termada observed exiting with code 0. The report does not capture terminal output, and agent notes are not independently verified. Verify the separate tamper-evident audit chain with `termada audit verify`.
