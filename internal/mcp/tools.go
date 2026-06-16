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
	strSchema   = map[string]any{"type": "string"}
	boolSchema  = map[string]any{"type": "boolean"}
	intSchema   = map[string]any{"type": "integer"}
	argvSchema  = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "command as an argv array, e.g. [\"echo\",\"hi\"]"}
	modeSchema  = map[string]any{"type": "string", "enum": []string{"auto", "foreground", "background"}}
	sigSchema   = map[string]any{"type": "string", "enum": []string{"SIGTERM", "SIGKILL", "SIGINT", "SIGHUP"}}
	emptySchema = map[string]any{"type": "object", "properties": map[string]any{}}
)

func (s *Server) registerTools() {
	mgr := s.backend
	// handlers read s.agentID live, since initialize may set it from clientInfo

	s.add(toolDef{
		Name:        "exec_run",
		Description: "Run a command and wait up to timeout_ms for it to finish, returning structured output. Long-running commands return with status running/backgrounded.",
		InputSchema: obj(map[string]any{
			"command":    argvSchema,
			"session":    strSchema,
			"mode":       modeSchema,
			"timeout_ms": intSchema,
		}, "command"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			argv, e := argArgv(a, "command")
			if e != nil {
				return nil, e
			}
			res, err := mgr.Run(s.agentID, argString(a, "session"), argv, argString(a, "mode"), argInt(a, "timeout_ms"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "exec_start",
		Description: "Start a command asynchronously and return immediately with a job_id. Poll with exec_poll.",
		InputSchema: obj(map[string]any{
			"command": argvSchema,
			"session": strSchema,
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
		Description: "Fetch incremental output from a job since the cursor, plus current status.",
		InputSchema: obj(map[string]any{
			"job_id": strSchema,
			"cursor": strSchema,
		}, "job_id"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.Poll(argString(a, "job_id"), argString(a, "cursor"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "exec_write",
		Description: "Send input to a running job's PTY (e.g. to answer a prompt). Set secret=true for passwords so the value is redacted and never logged.",
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
		Description: "List jobs filtered by active|recent|all (default all).",
		InputSchema: obj(map[string]any{"filter": map[string]any{"type": "string", "enum": []string{"active", "recent", "all"}}}),
		Handler: func(a map[string]any) (any, *errs.Error) {
			filter := argString(a, "filter")
			if filter == "" {
				filter = "all"
			}
			return map[string]any{"jobs": mgr.ListJobs(filter)}, nil
		},
	})

	s.add(toolDef{
		Name:        "session_create",
		Description: "Create a persistent-shell session that preserves cwd/env between commands. target=local, or a configured server name for a persistent remote SSH session.",
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
		Description: "Read a local file (secrets are best-effort redacted). Returns content, truncated and size.",
		InputSchema: obj(map[string]any{
			"path":      strSchema,
			"max_bytes": intSchema,
		}, "path"),
		Handler: func(a map[string]any) (any, *errs.Error) {
			res, err := mgr.FileRead(argString(a, "path"), argInt(a, "max_bytes"))
			return res, asErr(err)
		},
	})

	s.add(toolDef{
		Name:        "file_write",
		Description: "Write content to a local file. mode 'append' appends, otherwise truncates.",
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
