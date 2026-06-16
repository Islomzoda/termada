package engine

import (
	"path/filepath"
	"regexp"
	"strings"
)

// This file implements the execution heuristics (spec §13): classifying commands
// for adaptive timeouts (LR-2) and detecting long-running daemons so they are
// auto-backgrounded instead of blocking the agent (LR-1). All of it is
// overridable per call via exec_run's `mode` and `timeout_ms`.

// wrappers are leading tokens that wrap the real command; we look past them to
// classify (e.g. `sudo -E npm run build`, `env X=1 go test`).
var wrappers = map[string]bool{"sudo": true, "env": true, "time": true, "nice": true, "nohup": true, "stdbuf": true}

// realToken returns the first meaningful token of an argv, skipping wrappers and
// their inline options / VAR=val assignments.
func realToken(argv []string) (string, []string) {
	i := 0
	for i < len(argv) {
		t := argv[i]
		if strings.Contains(t, "=") && !strings.HasPrefix(t, "-") { // VAR=val
			i++
			continue
		}
		if wrappers[filepath.Base(t)] {
			i++
			// skip following options for the wrapper
			for i < len(argv) && strings.HasPrefix(argv[i], "-") {
				i++
			}
			continue
		}
		break
	}
	if i >= len(argv) {
		return "", nil
	}
	return filepath.Base(argv[i]), argv[i:]
}

var (
	reBuild   = regexp.MustCompile(`(?i)\b(make|cargo build|go build|go install|gradle|mvn|webpack|vite build|tsc|docker build|compose build|bazel build|cmake|ninja)\b`)
	reTest    = regexp.MustCompile(`(?i)\b(go test|pytest|cargo test|jest|mocha|rspec|phpunit|ctest|tox|vitest)\b|(npm|yarn|pnpm) (run )?test\b`)
	reInstall = regexp.MustCompile(`(?i)\b(apt|apt-get|yum|dnf|brew|pacman|pip|pip3|poetry|bundle|composer)\b|(npm|yarn|pnpm) (install|ci|i)\b|go mod (download|tidy)`)
	reDB      = regexp.MustCompile(`(?i)\b(psql|mysql|mongo|mongosh|redis-cli|pg_dump|pg_restore|mysqldump|sqlite3|clickhouse)\b`)
	reNetwork = regexp.MustCompile(`(?i)\b(curl|wget|scp|rsync|ping|ssh|sftp|nc|telnet)\b`)
	// long-running daemons / watchers that should auto-background quickly
	reDaemon = regexp.MustCompile(`(?i)\b(serve|server|dev|watch|nodemon|runserver|http\.server|webpack-dev|vite|ng serve|next dev|nuxt dev|hugo server|jekyll serve|flask run|gunicorn|uvicorn|rails server|php -S|ngrok)\b|compose up|\bup -d\b|tail -f|-f$|--follow|--watch`)
)

// classify returns the command's timeout class (build/install/test/db/network/
// default) for adaptive timeouts (LR-2).
func classify(argv []string) string {
	_, rest := realToken(argv)
	if len(rest) == 0 {
		return "default"
	}
	joined := strings.Join(rest, " ")
	switch {
	case reTest.MatchString(joined):
		return "test"
	case reBuild.MatchString(joined):
		return "build"
	case reInstall.MatchString(joined):
		return "install"
	case reDB.MatchString(joined):
		return "db"
	case reNetwork.MatchString(joined):
		return "network"
	default:
		return "default"
	}
}

// isDaemon reports whether the command looks like a long-running daemon/watcher
// that should be auto-backgrounded (LR-1).
func isDaemon(argv []string) bool {
	_, rest := realToken(argv)
	if len(rest) == 0 {
		return false
	}
	return reDaemon.MatchString(strings.Join(rest, " "))
}
