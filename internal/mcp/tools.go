package mcp

import (
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
)

type toolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(args map[string]any) (any, *errs.Error)
}

func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return out
}

func (s *Server) add(t toolDef) {
	s.tools[t.Name] = t
	s.order = append(s.order, t.Name)
}

// obj builds a JSON-schema object node.
func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

var (
	strSchema     = map[string]any{"type": "string"}
	boolSchema    = map[string]any{"type": "boolean"}
	intSchema     = map[string]any{"type": "integer"}
	sessionSchema = map[string]any{"type": "string", "description": "session id from session_create. Omit to use your per-agent default session (cwd/env still persist across your calls)."}
	argvSchema    = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "command as an argv array, e.g. [\"git\",\"status\"]. Each element is a literal arg — NOT a shell line (no $VAR, |, &&, globs). For shell features use [\"bash\",\"-lc\",\"<line>\"]."}
	modeSchema    = map[string]any{"type": "string", "enum": []string{"auto", "foreground", "background"}}
	sigSchema     = map[string]any{"type": "string", "enum": []string{"SIGTERM", "SIGKILL", "SIGINT", "SIGHUP"}}
	emptySchema   = map[string]any{"type": "object", "properties": map[string]any{}}
)

func (s *Server) registerTools() {
	mgr := s.backend
	// handlers read s.agentID live, since initialize may set it from clientInfo

	s.add(toolDef{
		Name:        "exec_run",
		Description: "Run a command and wait for it, returning {status, exit_code, stdout} (empty/false fields are omitted to stay light). `stdout` is the COMBINED stdout+stderr stream (the command runs on a PTY, so the two can't be separated) — if you need them apart, redirect inside the command, e.g. [\"bash\",\"-lc\",\"cmd 2>/tmp/err\"]. cwd and env PERSIST across calls in the same session — omit `session` to use your own per-agent default session (state still persists). NOTE: `command` is an argv array, not a shell line; $VARS, |, &&, >, globs and `cd x && y` are literal — for shell features use [\"bash\",\"-lc\",\"<line>\"]. A command still going past timeout_ms returns status=running/backgrounded with a job_id (plus waited_ms/timeout_ms so you can tell slow from hung) — stream it with exec_poll. A running/backgrounded job still occupies its session; create another session for parallel work.",
		InputSchema: obj(map[string]any{
			"command":    argvSchema,
			"session":    sessionSchema,
			"mode":       modeSchema,
			"timeout_ms": intSchema,
		}, "command"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			argv, e := argArgv(a, "command")
			if e != nil {
				return nil, e
			}
			res, err := mgr.Run(s.agentID, argString(a, "session"), argv, argString(a, "mode"), argInt(a, "timeout_ms"))
			if err != nil {
				return nil, asErr(err)
			}
			return leanRun(res), nil
		},
	})

	s.add(toolDef{
		Name:        "exec_start",
		Description: "Start a command asynchronously; returns {job_id, status} immediately. Stream output with exec_poll(job_id, cursor). Same argv/session rules as exec_run. (exec_run with mode=\"background\" does the same thing — use whichever reads clearer.) A background job still occupies its session; create another session for parallel work.",
		InputSchema: obj(map[string]any{
			"command": argvSchema,
			"session": sessionSchema,
			"mode":    modeSchema,
		}, "command"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			argv, e := argArgv(a, "command")
			if e != nil {
				return nil, e
			}
			snap, err := mgr.Start(s.agentID, argString(a, "session"), argv, argString(a, "mode"))
			if err != nil {
				return nil, asErr(err)
			}
			out := map[string]any{"job_id": snap.JobID, "status": snap.Status, "session_id": snap.SessionID}
			if snap.ConfirmationID != "" {
				out["confirmation_id"] = snap.ConfirmationID
			}
			return out, nil
		},
	})

	s.add(toolDef{
		Name:        "exec_poll",
		Description: "Fetch new output for a job since `cursor` (omit cursor to read from the start), plus status. Pass the returned next_cursor on your next poll. Set `wait_ms` to long-poll: the call then blocks (up to 30s) until there's new output, the job ends, or it needs input — so you can follow a job without a manual poll-sleep loop. status=awaiting_input means the command is blocked on stdin — answer it with exec_write. A terminal result can still have has_more=true when output was page-capped; keep polling its next_cursor until has_more is absent/false.",
		InputSchema: obj(map[string]any{
			"job_id":  strSchema,
			"cursor":  map[string]any{"type": "string", "description": "the next_cursor from your previous poll; omit to read from the start of retained output"},
			"wait_ms": map[string]any{"type": "integer", "description": "long-poll: block up to this many ms (capped at 30000) for new output / completion / input before returning; omit or 0 for an immediate non-blocking poll"},
		}, "job_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.Poll(s.agentID, argString(a, "job_id"), argString(a, "cursor"), argInt(a, "wait_ms"))
			if err != nil {
				return nil, asErr(err)
			}
			return leanPoll(res), nil
		},
	})

	s.add(toolDef{
		Name:        "exec_write",
		Description: "Answer a job blocked on stdin (use when a poll shows status=awaiting_input): sends `input` to the job's PTY. append_newline defaults true (presses Enter). For passwords, secret=true registers the exact input as a redaction literal and omits it from normal input logging; transformed or split echoes remain best-effort redaction.",
		InputSchema: obj(map[string]any{
			"job_id":         strSchema,
			"input":          strSchema,
			"append_newline": boolSchema,
			"secret":         boolSchema,
		}, "job_id", "input"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			err := mgr.Write(s.agentID, argString(a, "job_id"), argString(a, "input"), argBoolDefault(a, "append_newline", true), argBoolDefault(a, "secret", false))
			if err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(toolDef{
		Name:        "exec_signal",
		Description: "Signal a running job. Local PTY sessions send the requested SIGTERM/SIGKILL/SIGINT/SIGHUP to the command's real process group. Remote SSH PTYs cannot deliver named POSIX signals: SIGTERM, SIGKILL and SIGINT degrade to a best-effort Ctrl-C interrupt, while unsupported names such as SIGHUP return an error.",
		InputSchema: obj(map[string]any{
			"job_id": strSchema,
			"signal": sigSchema,
		}, "job_id", "signal"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			err := mgr.Signal(s.agentID, argString(a, "job_id"), argString(a, "signal"))
			if err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(toolDef{
		Name:        "exec_kill",
		Description: "Stop a running job. Local PTY sessions force-kill the command process group with SIGKILL. Remote SSH PTYs only receive a best-effort Ctrl-C interrupt, which is not a guaranteed force-kill.",
		InputSchema: obj(map[string]any{"job_id": strSchema}, "job_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			if err := mgr.Kill(s.agentID, argString(a, "job_id")); err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(toolDef{
		Name:        "exec_list",
		Description: "List your jobs, newest first. filter: active (only unfinished) | recent (default, the latest jobs) | all. Pass `limit` to widen the window (default 20). When older jobs are hidden, the response includes an `omitted` count.",
		InputSchema: obj(map[string]any{
			"filter": map[string]any{"type": "string", "enum": []string{"active", "recent", "all"}},
			"limit":  map[string]any{"type": "integer", "description": "max jobs to return, newest first (default 20, max 200)"},
		}),
		Handler: func(a map[string]any) (any, *errs.Error) {
			filter := argString(a, "filter")
			if filter == "" {
				filter = "recent"
			}
			limit := argInt(a, "limit")
			if limit <= 0 {
				if filter == "all" {
					limit = 100
				} else {
					limit = 20
				}
			}
			if limit > 200 {
				limit = 200
			}
			all := mgr.ListJobs(s.agentID, filter) // already newest-first, scoped to your jobs
			total := len(all)
			if len(all) > limit {
				all = all[:limit]
			}
			jobs := make([]map[string]any, len(all))
			for i, in := range all {
				jobs[i] = leanInfo(in)
			}
			out := map[string]any{"jobs": jobs}
			if total > len(jobs) {
				out["omitted"] = total - len(jobs)
			}
			return out, nil
		},
	})

	s.add(toolDef{
		Name:        "session_create",
		Description: "Create a named persistent-shell session that preserves cwd/env between commands. Optional — if you just want persistence you can skip this and let exec_run use your per-agent default session. Create one explicitly when you want a SECOND independent shell (e.g. a separate cwd/venv) or a remote one: target=local (default) or a configured server name for a persistent remote SSH session. Session creation is capped at 32 sessions per owner and 128 total; exceeding either limit returns parallelism_exceeded.",
		InputSchema: obj(map[string]any{
			"target": map[string]any{"type": "string", "description": "\"local\" (default) or a configured server name for a remote SSH session"},
			"mode":   map[string]any{"type": "string", "enum": []string{"shell"}},
		}),
		Handler: func(a map[string]any) (any, *errs.Error) {
			sess, err := mgr.CreateSession(s.agentID, argString(a, "target"), argString(a, "mode"))
			if err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"session_id": sess.SessionID, "target": sess.Target, "mode": sess.Mode, "owner": sess.Owner}, nil
		},
	})

	s.add(toolDef{
		Name:        "session_list",
		Description: "List active sessions.",
		InputSchema: emptySchema,
		Handler: func(a map[string]any) (any, *errs.Error) {
			return map[string]any{"sessions": mgr.ListSessions(s.agentID)}, nil
		},
	})

	s.add(toolDef{
		Name:        "session_close",
		Description: "Close a session and terminate its shell.",
		InputSchema: obj(map[string]any{"session_id": strSchema}, "session_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			if err := mgr.CloseSession(s.agentID, argString(a, "session_id")); err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(toolDef{
		Name:        "logs_tail",
		Description: "Return a job's output from the cursor (or the whole retained stream if no cursor).",
		InputSchema: obj(map[string]any{
			"job_id": strSchema,
			"cursor": strSchema,
		}, "job_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.Tail(s.agentID, argString(a, "job_id"), argString(a, "cursor"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "file_read",
		Description: "Read up to 1 MiB of a UTF-8 text file (secrets best-effort redacted). Returns {content, size}. Session-aware but NOT cwd-relative — pass an absolute path. Omit `session` to use the default local target, or pass a session_id whose target is local; local file tools are disabled on Windows and when security.run_as is enabled. With run_as, use exec_run in the dropped-uid local session instead. Pass a session_id whose target is remote to transfer text over SFTP without invoking a shell. The literal string `local` is not a session_id. This is not an arbitrary-binary API. Set max_bytes to a smaller prefix/read limit. `truncated:true` means the remainder was not returned; file_read has no cursor, so use a session command such as sed or dd to inspect later ranges.",
		InputSchema: obj(map[string]any{
			"path":      map[string]any{"type": "string", "description": "absolute path on the target host (not relative to any session's cwd)"},
			"session":   sessionSchema,
			"max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576},
		}, "path"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.FileRead(s.agentID, argString(a, "session"), argString(a, "path"), argInt(a, "max_bytes"))
			if err != nil {
				return nil, asErr(err)
			}
			return leanFileRead(res), nil
		},
	})

	s.add(toolDef{
		Name:        "file_write",
		Description: "Write UTF-8 text content to a file. mode 'append' appends, otherwise truncates. Session-aware but NOT cwd-relative — pass an absolute path. Omit `session` to use the default local target, or pass a session_id whose target is local; local file tools are disabled on Windows and when security.run_as is enabled. With run_as, use exec_run in the dropped-uid local session instead. Pass a session_id whose target is remote to transfer text over SFTP without invoking a shell (new files created 0600). The literal string `local` is not a session_id. This is not an arbitrary-binary API.",
		InputSchema: obj(map[string]any{
			"path":    strSchema,
			"content": strSchema,
			"session": sessionSchema,
			"mode":    map[string]any{"type": "string", "enum": []string{"truncate", "append"}},
		}, "path", "content"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.FileWrite(s.agentID, argString(a, "session"), argString(a, "path"), argString(a, "content"), argString(a, "mode"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "recipe_list",
		Description: "List configured command recipes.",
		InputSchema: emptySchema,
		Handler: func(a map[string]any) (any, *errs.Error) {
			return map[string]any{"recipes": mgr.RecipeList()}, nil
		},
	})

	s.add(toolDef{
		Name:        "recipe_run",
		Description: "Run a named recipe's steps in order, stopping on the first failure.",
		InputSchema: obj(map[string]any{
			"name":    strSchema,
			"session": strSchema,
		}, "name"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.RecipeRun(s.agentID, argString(a, "session"), argString(a, "name"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "server_list",
		Description: "List secret-free remote server inventory metadata: name, host, user, tags, managed flag, last status and checked_unix timestamp. Credential values and auth references are not returned.",
		InputSchema: emptySchema,
		Handler: func(a map[string]any) (any, *errs.Error) {
			return map[string]any{"servers": mgr.ServerList()}, nil
		},
	})

	s.add(toolDef{
		Name:        "fleet_run",
		Description: "Run a non-empty argv command across at most 256 servers selected by name and/or tag, returning per-server results with stdout/stderr/errors best-effort redacted and a truncated flag when the 2 MiB aggregate text budget is reached. Fleet work shares a manager-wide concurrency ceiling across simultaneous fleet calls (production ceiling 5); requested parallelism can only lower this call's concurrency. Not atomic.",
		InputSchema: obj(map[string]any{
			"command":     argvSchema,
			"servers":     map[string]any{"type": "array", "items": strSchema},
			"tags":        map[string]any{"type": "array", "items": strSchema},
			"parallelism": intSchema,
		}, "command"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			argv, e := argArgv(a, "command")
			if e != nil {
				return nil, e
			}
			selector := append(argStrings(a, "servers"), argStrings(a, "tags")...)
			res, err := mgr.FleetRun(s.agentID, argv, selector, argInt(a, "parallelism"))
			return res, asErr(err)
		},
	})

	// Dynamic plugin tools (spec §29): each plugin-provided tool is registered as
	// "<plugin>.<tool>" and dispatched to the out-of-process plugin via the
	// backend, passing through the same policy/audit as built-in tools.
	for _, pt := range mgr.PluginTools() {
		name := pt.Name
		schema := pt.InputSchema
		if schema == nil {
			schema = emptySchema
		}
		s.add(toolDef{
			Name:        name,
			Description: "[plugin] " + pt.Description,
			InputSchema: schema,
			Handler: func(a map[string]any) (any, *errs.Error) {
				res, err := mgr.PluginCall(s.agentID, name, a)
				return res, asErr(err)
			},
		})
	}

	s.add(toolDef{
		Name:        "port_forward",
		Description: "Open a local→remote TCP tunnel through a configured server (like `ssh -L`): the returned local_addr on the daemon host forwards to remote_host:remote_port reached FROM that server — e.g. to reach a DB bound to a remote box's localhost. Stays open until port_forward_close; live forwards are capped at 16 per owner and 64 total. The remote sshd must allow TCP forwarding.",
		InputSchema: obj(map[string]any{
			"server":      map[string]any{"type": "string", "description": "configured server name to tunnel through"},
			"remote_host": map[string]any{"type": "string", "description": "host to reach FROM the server (often 127.0.0.1)"},
			"remote_port": intSchema,
			"local_bind":  map[string]any{"type": "string", "description": "loopback listen address only; default 127.0.0.1:0 (auto port); non-loopback binds are refused"},
		}, "server", "remote_host", "remote_port"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.PortForward(s.agentID, argString(a, "server"), argString(a, "remote_host"), argInt(a, "remote_port"), argString(a, "local_bind"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "port_forward_list",
		Description: "List the live port forwards (id, server, remote target, local_addr).",
		InputSchema: emptySchema,
		Handler: func(a map[string]any) (any, *errs.Error) {
			return map[string]any{"forwards": mgr.PortForwardList(s.agentID)}, nil
		},
	})

	s.add(toolDef{
		Name:        "port_forward_close",
		Description: "Close a port forward by its id (from port_forward / port_forward_list).",
		InputSchema: obj(map[string]any{"id": strSchema}, "id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			if err := mgr.PortForwardClose(s.agentID, argString(a, "id")); err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(toolDef{
		Name:        "capabilities",
		Description: "Report the asserted MCP client id, API version, available tools and execution modes. agent_id is client-reported, not proof of daemon ownership; when configured, the transport token is authoritative.",
		InputSchema: emptySchema,
		Handler: func(a map[string]any) (any, *errs.Error) {
			remote := mgr.RemoteAvailable()
			execMode := "in-process"
			notes := "internal local backend: remote SSH sessions, fleet, vault and plugins are unavailable; session_create accepts target=local only. Production MCP startup requires the daemon."
			if remote {
				execMode = "daemon"
				notes = "daemon-backed: local + remote persistent shells (session_create target=<server>), fleet_run, vault and plugins are available. Inspect inventory with server_list; configure servers in the dashboard or config file. agent_id is asserted client metadata; a configured transport token is authoritative for daemon ownership."
			}
			return map[string]any{
				"agent_id":    s.agentID,
				"api_version": "0.x",
				"tools":       s.order,
				"modes":       []string{engine.ModeAuto, engine.ModeForeground, engine.ModeBackground},
				// remote tells the agent whether a non-local target can work at
				// all, so it never silently settles for a local shell when it
				// meant to reach a server.
				"remote":     remote,
				"exec_mode":  execMode,
				"quickstart": "commands are argv arrays, not shell lines ($VAR/pipes/&&/globs are literal — use [\"bash\",\"-lc\",\"...\"] for shell features). cwd & env persist across calls in a session; omit `session` to use your per-agent default session (always LOCAL). To act on a server, session_create target=<server> and pass the returned session_id to exec_run. exec_run waits and returns stdout; exec_start returns a job_id you stream with exec_poll(job_id, next_cursor). Keep following next_cursor while has_more=true, even after terminal status. When status=awaiting_input, reply with exec_write. Responses omit empty/false fields to stay light; errors carry a `hint`.",
				"notes":      notes,
			}, nil
		},
	})
}

// asErr narrows a generic error to *errs.Error for the tool result.
func asErr(err error) *errs.Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*errs.Error); ok {
		return e
	}
	return errs.New(errs.Internal, "%v", err)
}

func argString(a map[string]any, k string) string {
	if v, ok := a[k].(string); ok {
		return v
	}
	return ""
}

func argBoolDefault(a map[string]any, k string, d bool) bool {
	if v, ok := a[k].(bool); ok {
		return v
	}
	return d
}

func argInt(a map[string]any, k string) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func argStrings(a map[string]any, k string) []string {
	raw, ok := a[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if sv, ok := e.(string); ok {
			out = append(out, sv)
		}
	}
	return out
}

func argArgv(a map[string]any, k string) ([]string, *errs.Error) {
	raw, ok := a[k]
	if !ok {
		return nil, errs.New(errs.InvalidArgument, "%s is required", k)
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errs.New(errs.InvalidArgument, "%s must be an array of argv strings, e.g. [\"echo\",\"hi\"]", k)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		sv, ok := e.(string)
		if !ok {
			return nil, errs.New(errs.InvalidArgument, "%s elements must be strings", k)
		}
		out = append(out, sv)
	}
	if len(out) == 0 {
		return nil, errs.New(errs.InvalidArgument, "%s must not be empty", k)
	}
	return out, nil
}
