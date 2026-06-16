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
		Description: "Run a command and wait for it, returning {status, exit_code, stdout} (empty/false fields are omitted to stay light). cwd and env PERSIST across calls in the same session — omit `session` to use your own per-agent default session (state still persists). NOTE: `command` is an argv array, not a shell line; $VARS, |, &&, >, globs and `cd x && y` are literal — for shell features use [\"bash\",\"-lc\",\"<line>\"]. A command still going past timeout_ms returns status=running/backgrounded with a job_id — stream it with exec_poll.",
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
		Description: "Start a command asynchronously; returns {job_id, status} immediately. Stream output with exec_poll(job_id, cursor). Same argv/session rules as exec_run. (exec_run with mode=\"background\" does the same thing — use whichever reads clearer.)",
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
		Description: "Fetch new output for a job since `cursor` (omit cursor to read from the start), plus status. Pass the returned next_cursor on your next poll. status=awaiting_input means the command is blocked on stdin — answer it with exec_write. When status is terminal (exited/killed/…), there is no next_cursor and nothing more to poll.",
		InputSchema: obj(map[string]any{
			"job_id": strSchema,
			"cursor": map[string]any{"type": "string", "description": "the next_cursor from your previous poll; omit to read from the start of retained output"},
		}, "job_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.Poll(argString(a, "job_id"), argString(a, "cursor"))
			if err != nil {
				return nil, asErr(err)
			}
			return leanPoll(res), nil
		},
	})

	s.add(toolDef{
		Name:        "exec_write",
		Description: "Answer a job blocked on stdin (use when a poll shows status=awaiting_input): sends `input` to the job's PTY. append_newline defaults true (presses Enter). Set secret=true for passwords so the value is redacted and never logged.",
		InputSchema: obj(map[string]any{
			"job_id":         strSchema,
			"input":          strSchema,
			"append_newline": boolSchema,
			"secret":         boolSchema,
		}, "job_id", "input"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			err := mgr.Write(argString(a, "job_id"), argString(a, "input"), argBoolDefault(a, "append_newline", true), argBoolDefault(a, "secret", false))
			if err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(toolDef{
		Name:        "exec_signal",
		Description: "Send a signal (SIGTERM/SIGKILL/SIGINT/SIGHUP) to a running job's process group.",
		InputSchema: obj(map[string]any{
			"job_id": strSchema,
			"signal": sigSchema,
		}, "job_id", "signal"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			err := mgr.Signal(argString(a, "job_id"), argString(a, "signal"))
			if err != nil {
				return nil, asErr(err)
			}
			return map[string]any{"ok": true}, nil
		},
	})

	s.add(toolDef{
		Name:        "exec_kill",
		Description: "Force-kill a running job (SIGKILL to its process group).",
		InputSchema: obj(map[string]any{"job_id": strSchema}, "job_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			if err := mgr.Kill(argString(a, "job_id")); err != nil {
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
			all := mgr.ListJobs(filter) // already newest-first
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
		Description: "Create a named persistent-shell session that preserves cwd/env between commands. Optional — if you just want persistence you can skip this and let exec_run use your per-agent default session. Create one explicitly when you want a SECOND independent shell (e.g. a separate cwd/venv) or a remote one: target=local (default) or a configured server name for a persistent remote SSH session.",
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
			return map[string]any{"sessions": mgr.ListSessions()}, nil
		},
	})

	s.add(toolDef{
		Name:        "session_close",
		Description: "Close a session and terminate its shell.",
		InputSchema: obj(map[string]any{"session_id": strSchema}, "session_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			if err := mgr.CloseSession(argString(a, "session_id")); err != nil {
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
			res, err := mgr.Tail(argString(a, "job_id"), argString(a, "cursor"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "file_read",
		Description: "Read a file on the daemon host (secrets best-effort redacted). Returns {content, size}. NOT session-scoped — a session's cwd does not apply, so pass an absolute path. Large files are capped (set max_bytes); a `truncated:true` flag appears only when content was cut.",
		InputSchema: obj(map[string]any{
			"path":      map[string]any{"type": "string", "description": "absolute path on the daemon host (not relative to any session's cwd)"},
			"max_bytes": intSchema,
		}, "path"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.FileRead(argString(a, "path"), argInt(a, "max_bytes"))
			if err != nil {
				return nil, asErr(err)
			}
			return leanFileRead(res), nil
		},
	})

	s.add(toolDef{
		Name:        "file_write",
		Description: "Write content to a file on the daemon host. mode 'append' appends, otherwise truncates. NOT session-scoped — pass an absolute path (a session's cwd does not apply).",
		InputSchema: obj(map[string]any{
			"path":    strSchema,
			"content": strSchema,
			"mode":    map[string]any{"type": "string", "enum": []string{"truncate", "append"}},
		}, "path", "content"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.FileWrite(argString(a, "path"), argString(a, "content"), argString(a, "mode"))
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
		Description: "List configured remote servers (names/hosts/tags only — no secrets).",
		InputSchema: emptySchema,
		Handler: func(a map[string]any) (any, *errs.Error) {
			return map[string]any{"servers": mgr.ServerList()}, nil
		},
	})

	s.add(toolDef{
		Name:        "fleet_run",
		Description: "Run a command across servers selected by name and/or tag, returning per-server results. Not atomic.",
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
			res, err := mgr.FleetRun(argv, selector, argInt(a, "parallelism"))
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
				res, err := mgr.PluginCall(name, a)
				return res, asErr(err)
			},
		})
	}

	s.add(toolDef{
		Name:        "capabilities",
		Description: "Report this agent's identity, the API version, available tools and execution modes.",
		InputSchema: emptySchema,
		Handler: func(a map[string]any) (any, *errs.Error) {
			return map[string]any{
				"agent_id":    s.agentID,
				"api_version": "0.x",
				"tools":       s.order,
				"modes":       []string{engine.ModeAuto, engine.ModeForeground, engine.ModeBackground},
				"quickstart":  "commands are argv arrays, not shell lines ($VAR/pipes/&&/globs are literal — use [\"bash\",\"-lc\",\"...\"] for shell features). cwd & env persist across calls in a session; omit `session` to use your per-agent default session. exec_run waits and returns stdout; exec_start returns a job_id you stream with exec_poll(job_id, next_cursor). When status=awaiting_input, reply with exec_write. Responses omit empty/false fields to stay light; errors carry a `hint`.",
				"notes":       "phase 1: local persistent-shell engine; SSH/fleet/vault are phase 2",
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
