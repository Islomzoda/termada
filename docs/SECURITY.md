# Termada security and threat model

Termada gives explicit, best-effort guarantees rather than claiming that an AI
agent running commands is harmless. This document describes the current trust
boundaries and the controls that enforce them.

## Trust boundaries

- **MCP agents are untrusted callers.** Agent requests receive a transport
  principal (token-bound when configured, self-asserted only in local/dev mode).
  Ownership checks limit resource access to that owner's jobs, sessions, remote
  files addressed through those sessions, and port forwards. Agents can also
  invoke protected-path-guarded local file operations and policy-authorized
  recipe steps, fleet commands, plugin calls, and forward creation.
  Local host paths are shared according to OS permissions and the file-tool deny
  roots; they are not a per-agent filesystem namespace.
- **The dashboard and human CLI are operator surfaces.** They can inspect global
  state, approve or deny commands, write to sessions, stop jobs, manage policies
  and servers, unlock the vault, and create or restore snapshots.
- **Plugins are trusted local executables.** They are not sandboxed. A plugin has
  the filesystem and network permissions of the daemon user, even though Termada
  starts it with a minimal environment and bounded resources. See
  [PLUGINS.md](PLUGINS.md).
- **The host OS remains the outer boundary.** A local root or the daemon's own uid
  can inspect process memory and files. `security.run_as` gives local shell
  processes a separate OS identity and disables local file-tool operations that
  would otherwise run with the daemon uid.

## Transport authentication

### TCP dashboard and API

Every TCP path under `/api/*` and `/metrics` requires the dashboard token. The
token may be supplied as a Bearer token or in the tokenized dashboard URL printed
by `termada serve` and `termada dashboard`; the SPA moves it to browser session
storage after bootstrap. Static dashboard assets contain no runtime data and are
served without the token.

Loopback Host and Origin checks are also enforced to reduce DNS-rebinding risk.
They are defense in depth, not a substitute for the token. The legacy
`dashboard.local_trust` option is accepted for configuration compatibility but
does not grant an API principal or bypass authentication.

Bind the native daemon to loopback. The Docker image binds `0.0.0.0` only inside
the container so the host can reach it; publish it with
`-p 127.0.0.1:7717:7717`, not on every host interface.

### Unix control socket

The control socket is mode `0600` and is shared by MCP stdio shims and the CLI.
Merely reaching it does not make a caller an operator:

- an MCP shim presents its optional `X-Termada-Agent-Token`; a valid configured
  token determines the agent id and overrides any asserted owner;
- an id that has a configured token cannot be claimed without that token, and an
  unknown presented token is rejected;
- ids without token bindings use the MCP client's asserted id. This is an honest
  local/development fallback, not strong identity authentication;
- global inspection and operator routes require the separate `cli.token`, which
  the human CLI reads from the mode-`0600` runtime file. The MCP shim never sends
  it.

Owner checks are enforced in the control plane and engine, so changing an
`owner` field does not grant access to another authenticated agent's resources.

The default same-uid deployment weakens the socket-token boundary: an agent can
run a shell command that reads files available to the daemon uid, including its
tokens. `file_read` blocks those paths, but raw `exec` is an OS process and does
not use the file-tool guard. Running the daemon as root with `security.run_as` set
to a dedicated unprivileged uid reduces what local shell processes can read;
startup fails if the requested uid separation cannot be applied. In this mode,
local `file_read` and `file_write` fail closed instead of acting with root/daemon
permissions; use `exec` inside the dropped-uid local session for project file
access. Remote file operations through an owner-scoped SSH session still use
SFTP. Trusted plugins still run as the daemon uid, so use a separate container/VM
or least-privileged daemon instance when a hard boundary is required.

## Enforced controls

| Control | Current behavior |
| --- | --- |
| Command policy | Built-in commands are argv arrays and every word is shell-quoted, including an assignment-looking executable word. Allow rules are strict, case-exact whitelists. Deny/confirm matching also inspects absolute program paths, leading assignments, known wrappers, and explicit shell `-c` payloads. A shell script, stdin-driven/interactive shell, ambiguous control syntax, or uninspectable wrapper fails closed when deny/confirm rules are present. Darwin case-folding broadens only deny/confirm gates, never allow. Confirmation times out to deny. |
| Confirmation scope | Confirm-matched session commands and recipe steps use the human approval queue. `fleet_run`, plugin calls and forward creation have no queued approval flow, so a confirm decision fails closed rather than executing unsupervised. |
| Recipes | Recipe definitions are bounded in count, steps and aggregate argv size. A timed-out step is interrupted; if it remains live after five seconds, Termada closes its session and forces a terminal local lifecycle (an uncontrolled remote process may still continue). Each result includes its job id/reason, and stdout across the whole recipe is capped to one output page. |
| Agent isolation | Jobs, sessions, input, output, recipes, fleet actions, plugin calls, forwards, and remote file operations through a session are attributed to the transport-derived owner. Cross-owner resource lookups are rejected. Local host paths remain shared. Per-agent and global foreground/background quotas are reserved before shell I/O. Sessions are capped at 32 per owner/128 total, pending confirmations at 32/128, and the in-memory job registry at 256. |
| Operator actions | TCP operator actions require the dashboard token. Unix-socket operator actions require `cli.token`; caller-supplied `human` or `owner` flags do not confer privilege. |
| Protocol inputs | Control-plane request bodies, newline-delimited MCP messages, and encoded MCP responses are capped at 8 MiB. Interactive input is capped at 64 KiB and blocking PTY/SSH writes have finite deadlines. Invalid, unknown, trailing or oversized control-plane JSON is rejected before a handler acts. HTTP headers have a five-second deadline and each listener admits at most 256 connections. |
| Vault | The only supported backend is an age-encrypted file. Secret values are internal-only and registered for output redaction after unlock. Daemon unlock attempts are serialized; five consecutive failures trigger a 30-second lockout. Optional `vault.idle_relock_ms` locks the vault and clears its own passphrase/value map after inactivity; exact copies already registered with the redactor remain in daemon memory so later output can still be masked. |
| File tools | Local operations deny runtime tokens, vault/audit/registry files, `~/.ssh`, `~/.aws`, `~/.gnupg`, and configured `security.protected_paths`. Unix paths are canonicalized for the deny check, Darwin comparisons are case-folded, and a protected filesystem root covers every descendant. Paths are then opened by descriptor walk with no-follow checks so a symlink swap cannot redirect the operation; FIFO/device/special files are refused. Local file tools fail closed on Windows and whenever `security.run_as` separates shell uid from daemon uid. Reads otherwise return at most a 1 MiB prefix (100,000-byte default), and new files default to mode `0600`. Unprotected local paths are shared by agents, and neither this guard nor its protected roots apply to arbitrary `exec` commands or remote SFTP paths. |
| Secret input and output | `exec_write(secret=true)` is excluded from normal input logging and registered as an exact redaction literal. Built-in patterns and vault literals are redacted from terminal, file and fleet output and from plugin process errors. Audit redaction walks nested maps, arrays and strings. Replacement masks never contain more bytes than their match, so redaction cannot amplify a bounded event past the audit limit. Exact-literal work is capped at 32 MiB of scanning per value; above that budget the entire value is masked in linear time. Successful plugin JSON results are not automatically redacted. Redaction remains best-effort. |
| Audit | Audit delivery is a synchronous reliable bus sink rather than a drop-on-overflow queue. Events are rejected above 512 KiB before reaching audit or the in-memory ring. Each append is hash-chained, flushed and fsynced. Ordinary commands record a durable start intent before touching the shell; confirmation-approved commands, fleet/plugin calls and forward creation likewise fail closed if their required pre-execution record cannot be written. Storage/marshal failures permanently mark audit unhealthy; an oversized caller record is rejected without poisoning later valid appends. Outcome events that fail after an irreversible operation latch unhealthy state for later protected operations. Rotation preserves one sequence/hash chain and retains at most 64 sealed segments plus the active file by default. Before pruning an older prefix, an atomically replaced checkpoint records its terminal sequence/hash and a rolling SHA-256 commitment over the exact removed segment bytes. `termada audit verify` validates that checkpoint and every retained segment through the active file; audit-tail responses have a 4 MiB aggregate budget. |
| Missions | Mission records are owner-scoped, mode `0600`, and bounded to 128 missions, 24 steps and 512 projected events per mission. Terminal output is not copied. A passed step must reference an `exited/0` job from one of the mission's session attempts, and success is refused while any mission job remains active. Audit is written before the mission projection. Reports include retained audit sequence/hash anchors and record their own SHA-256 as an audit event. |
| SSH | Host keys use trust on first use. Updates to the Termada `known_hosts` file are serialized and fsynced; malformed existing data and key mismatches fail closed. One-shot/fleet command stdout and stderr are each capped at 1 MiB. Persistent remote shells cannot address a remote process group directly: supported interrupt/kill requests send best-effort PTY Ctrl-C and are not a guaranteed force-kill. |
| Fleet | `fleet_run` rejects an empty argv and more than 256 matched targets. All simultaneous calls share the manager-wide ceiling of 5 active targets; a caller may request less concurrency but cannot raise it. Per-server stdout/stderr are each capped at 1 MiB and the returned aggregate at 2 MiB. Results are not atomic. |
| Port forwarding | Creation is owner-scoped and policy-gated, and a forward is rolled back if its start event cannot be durably audited. Listing and closing are owner-scoped; close emits a lifecycle event but is not re-evaluated by command policy. Local listeners are restricted to loopback, remote ports must be in `1..65535`, each forward accepts at most 64 connections, and live forwards are capped at 16 per owner/64 total. |
| Plugins | Only regular executable files are loaded and symlinks are refused. Unix files must be executable and not writable by group/others; Windows requires `.exe` and relies on the directory ACL. Executables are capped at 64 MiB; file identity and a SHA-256 content digest are checked around discovery and invocation to detect stable replacements and in-place changes. These checks are not code signing or an atomic sandbox boundary against a malicious writer racing execution. Names, schemas, tool count, input/output/stderr sizes and execution time are bounded, with at most eight plugin calls executing concurrently. Timeouts terminate the main process and attempt best-effort descendant cleanup with a Unix process group or Windows Job Object. Calls are policy-gated as `plugin <plugin.tool>` and do not start unless the start event is durably audited; outcome append failures latch audit unhealthy for later protected operations. A confirm decision fails closed because interactive plugin approval is not implemented. |
| Snapshots | Snapshots are local-file-system only and capped at 200 MiB across the tree. Sources must contain only regular files and directories; symlinks, devices and other special entries make creation fail rather than producing an incomplete snapshot. Snapshot ids cannot traverse the store, and restore stages a copy then swaps it into place with rollback on rename failure. |
| Install/update | `install.sh` and Unix self-update refuse releases without a matching SHA-256 checksum, bound metadata/archive sizes, and extract only the expected binary. If a release public key is configured, the checksum signature is mandatory. Installer signature verification also requires an OpenSSL build with Ed25519 `pkeyutl -rawin` support and fails closed without it. Unix replacement is atomic; Windows self-update returns an explicit manual-install instruction because a running `.exe` cannot be replaced atomically. |

## Important limits

- **Mission reports are evidence summaries, not remote attestation.** Runtime
  events and audit anchors show what this daemon observed; agent summaries and
  notes remain agent-supplied. The report excludes terminal output. Its SHA-256
  is anchored only in the same local audit boundary, so a host-level attacker
  who can rewrite the whole audit can also rewrite that evidence. Use an
  external immutable anchor when stronger proof is required.
- **File tools are not yet part of mission command evidence.** `file_read` and
  `file_write` have owner/protected-path controls but do not share the complete
  command policy/audit lifecycle. Use `exec_*` in the dedicated mission session
  for actions that must appear in the current evidence report.

- **Local paths are not owner-isolated.** Every agent with file-tool access can
  address the same unprotected host paths. Use OS accounts, permissions and
  separate daemon instances when projects require filesystem isolation.
- **`security.run_as` changes the local file workflow.** It reduces the OS
  permissions of local sessions and makes local `file_read`/`file_write` fail
  closed, because those tools otherwise execute in the daemon process. Use
  dropped-uid session commands for local files. Dropped shells receive a fixed
  minimal environment (`HOME`, identity, system `PATH`, terminal and C locale),
  not the daemon's arbitrary environment or credential variables. Remote SFTP
  remains available. This still is not a plugin sandbox or a complete host
  isolation boundary.
- **Same-uid execution can read same-uid data.** A clean daemon environment and
  least-privilege OS deployment reduce exposure; use an isolated daemon instance
  or container/VM when protecting host credentials from an agent is mandatory.
- **Unbound agent ids are self-asserted.** Configure a unique token of at least
  16 characters for every identity that crosses a trust boundary, and pass it to
  the shim with `serve --stdio --token` or `TERMADA_AGENT_TOKEN`.
- **The production shim requires the daemon.** It may auto-start the daemon, but
  refuses to run commands if that fails; there is no policy/audit-bypassing
  in-process fallback.
- **Plugins can do anything the daemon uid can do.** A minimal environment,
  process-tree cleanup and limits reduce accidental exposure and resource leaks;
  they do not provide filesystem, network, syscall or privilege isolation.
  Trusted code can deliberately detach descendants (and a Windows child can race
  Job Object assignment). Plugin JSON results are returned to the agent without
  automatic redaction, so a plugin must never return secrets.
- **Redaction cannot recognize arbitrary secrets.** Known formats and registered
  exact values are masked, but transformed, split or previously unknown secrets
  can escape. Do not place secrets in command arguments or plugin results.
- **Policy is not a language or syscall sandbox.** An explicitly allowlisted
  interpreter, programmable editor, build tool or other interactive program can
  perform actions that are not visible in its original argv. Keep allowlists
  narrow and use `security.run_as`, OS permissions or an isolated daemon when
  the executed program itself is not trusted.
- **TOFU cannot authenticate the first SSH connection.** Verify the pinned host
  key out of band when first-connect interception is in scope.
- **Tamper evidence is not tamper resistance.** An attacker able to rewrite the
  entire chain from genesis can produce a different valid chain. Stronger
  guarantees require anchoring a chain head outside these files. Without that
  external head, deleting the entire log or truncating a valid suffix of the
  active segment is not distinguishable from a shorter legitimate history.
- **Audit retention commits to pruned data but does not preserve it.** The local
  checkpoint binds the retained suffix to the removed prefix and commits to each
  removed segment's bytes, but verification cannot replay records whose segment
  has been pruned. Copy sealed segments and checkpoints to an immutable external
  archive before the 64-segment window advances when full record retention is a
  compliance requirement. Without an external anchor, an attacker able to
  rewrite both log and checkpoint has the same whole-history limitation above.
- **Secret material lives in daemon memory.** Unlocked vault values and
  passphrases are not protected from local root, ptrace, crash dumps or a trusted
  plugin with equivalent privileges. Exact secret copies registered for
  redaction remain until daemon exit even after the vault relocks.
- **Vault lockout is online-only.** The five-attempt throttle protects the
  daemon unlock endpoint, not an attacker who can copy `vault.age` and attempt
  passphrases offline. Use a strong passphrase and OS file permissions.
- **SSH reconnect starts a fresh remote shell.** Prior cwd/env are lost and the
  in-flight job becomes `orphaned`, so Termada no longer has a trustworthy result
  or control channel. The remote process may still continue after the connection
  drops. Verify remote state before retrying any non-idempotent operation; the
  reconnected session can serve new commands independently.
- **Stop-All covers active engine jobs only.** It also cancels jobs parked for
  confirmation, but it does not close SSH forwards or cancel already-running
  one-shot fleet/plugin calls. Close forwards explicitly and verify external
  effects separately.
- **Snapshots are not transactions.** They do not undo remote hosts, databases,
  network effects, subprocess side effects or concurrent changes outside the
  copied local tree.
- **Release signatures are conditional.** SHA-256 verification is always
  required, but it authenticates the binary only as strongly as the downloaded
  checksum file. Configure/embed the Ed25519 public key to require signed
  checksums.

## Configuration parsing

Configuration uses a strict YAML schema: unknown fields and unsupported values
are errors. `${NAME}` references are expanded only in parsed YAML string scalar
values, so environment contents cannot inject YAML structure; an unset active
reference makes startup fail instead of silently producing an empty credential.
Telegram integration is outbound-only and accepts only a bot token
and destination chat id. The OS keychain, inbound Telegram user allowlists,
`vault.unlock_ttl_ms`, non-stdio agent transports and non-TCP dashboard sockets
are not implemented.

## Reporting

This is a 0.x project and a formal external security review is still pending.
Report vulnerabilities through the GitHub repository without including live
credentials or sensitive audit data.
