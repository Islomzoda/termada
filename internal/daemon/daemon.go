// Package daemon is the long-lived termada process (spec R4): it owns the
// engine, event bus, audit log and vault, and serves the control-plane over a
// Unix socket (local trust, for the stdio shim/CLI/TUI) and the dashboard over
// loopback TCP with a token.
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/config"
	"github.com/termada/termada/internal/controlplane"
	"github.com/termada/termada/internal/dashboard"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/notify"
	"github.com/termada/termada/internal/plugin"
	"github.com/termada/termada/internal/policy"
	"github.com/termada/termada/internal/sshx"
	"github.com/termada/termada/internal/vault"
)

// RuntimeDir is where the socket, token and audit log live.
func RuntimeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "termada")
}

// SocketPath is the control-plane Unix socket.
func SocketPath() string { return filepath.Join(RuntimeDir(), "termada.sock") }

// TokenPath is the dashboard token file.
func TokenPath() string { return filepath.Join(RuntimeDir(), "token") }

// CLITokenPath is the human-CLI auth token for the local control socket. It
// gates the human approval routes (approve/deny/stop_all) on the UDS so an agent
// shelling out to curl the socket cannot self-approve a parked command.
func CLITokenPath() string { return filepath.Join(RuntimeDir(), "cli.token") }

// AuditPath is the audit log file.
func AuditPath() string { return filepath.Join(RuntimeDir(), "audit.log") }

// Daemon bundles the running services.
type Daemon struct {
	cfg      config.Config
	version  string
	logger   *log.Logger
	mgr      *engine.Manager
	bus      *bus.Bus
	audit    *audit.Logger
	vault    *vault.Vault
	fleet    *fleet.Manager
	plugins  *plugin.Manager
	token    string
	cliToken string
}

// New builds a daemon from config.
func New(cfg config.Config, version string, logger *log.Logger) (*Daemon, error) {
	if err := os.MkdirAll(RuntimeDir(), 0o700); err != nil {
		return nil, err
	}
	b := bus.New(500)
	aud, err := audit.Open(AuditPath(), nil)
	if err != nil {
		return nil, fmt.Errorf("open audit: %w", err)
	}

	ec := engine.Config{
		OutputRetentionBytes: cfg.Defaults.OutputRetentionBytes,
		MaxForegroundJobs:    cfg.Defaults.MaxForegroundJobs,
		MaxJobsPerAgent:      cfg.Defaults.MaxJobsPerAgent,
		MaxJobRuntimeMS:      cfg.Defaults.MaxJobRuntimeMS,
		DefaultTimeoutMS:     cfg.Defaults.TimeoutMS,
		ConfirmTimeoutMS:     cfg.Defaults.ConfirmTimeoutMS,
		RedactionPatterns:    cfg.Redaction,
	}
	mgr := engine.NewManager(ec)
	mgr.SetBus(b)
	pol := policy.NewEngine(buildPolicies(cfg))
	pol.LoadStore(filepath.Join(RuntimeDir(), "policies.json"))
	mgr.SetPolicy(pol, buildAgentPolicies(cfg))
	mgr.SetAgentTokens(buildAgentTokens(cfg))
	mgr.SetRecipes(buildRecipes(cfg))
	mgr.SetTimeoutClasses(cfg.TimeoutClasses)
	// file_read/file_write refuse the daemon's own secrets (tokens, vault) and the
	// usual host credential stores, so an agent can't exfiltrate them — including
	// the cli.token that gates self-approval — through the file API (C2/FS-3).
	mgr.SetProtectedPaths(buildProtectedPaths(cfg))
	// Optionally drop agent shells to a less-privileged uid so their `exec` can't
	// read those secrets either (SEC-8). Fail closed: if the operator asked for
	// uid separation but we can't provide it (not root), refuse to start rather
	// than silently running agents with full privileges.
	spawn, err := resolveSpawn(cfg.Security.RunAs)
	if err != nil {
		return nil, err
	}
	mgr.SetSpawnConfig(spawn)
	if spawn.SeparateUID {
		logger.Printf("uid separation ON: agent sessions run as uid=%d gid=%d", spawn.UID, spawn.GID)
	}
	if err := mgr.EnablePersistence(filepath.Join(RuntimeDir(), "registry.json")); err != nil {
		logger.Printf("warning: registry recovery failed: %v", err)
	}
	mgr.SetSnapshotDir(filepath.Join(RuntimeDir(), "snapshots"))

	// audit uses the same redactor as the engine so secrets are masked in the log.
	aud.SetRedactor(mgr.Redactor())
	// dangerous commands fail closed if the audit log can't record them (RE-7).
	mgr.SetAuditHealth(aud.Healthy)

	v := vault.New(config.ExpandPath(cfg.Vault.File))
	runner := sshx.NewRunner(v, filepath.Join(RuntimeDir(), "known_hosts"), 20*time.Second)
	fl := fleet.New(buildServers(cfg), runner, 5)
	fl.LoadStore(filepath.Join(RuntimeDir(), "servers.json"))
	// enable persistent remote sessions: session_create against a server name
	// opens a shell over SSH (spec §14/P-10).
	mgr.SetRemoteDialer(func(target string, cols, rows int) (engine.ShellConn, error) {
		srv, ok := fl.Get(target)
		if !ok {
			// Spell out what IS configured so the agent fixes the name (or adds
			// the server) instead of silently giving up and using a local shell.
			known := make([]string, 0)
			for _, s := range fl.ServerList() {
				known = append(known, s.Name)
			}
			if len(known) == 0 {
				return nil, fmt.Errorf("no server named %q (no servers configured — add one in the dashboard or via /api/servers/add)", target)
			}
			return nil, fmt.Errorf("no server named %q (configured: %s)", target, strings.Join(known, ", "))
		}
		return runner.OpenShell(srv, cols, rows)
	})

	plugins := plugin.New(filepath.Join(RuntimeDir(), "plugins"))
	if err := plugins.Load(); err != nil {
		logger.Printf("warning: plugin load: %v", err)
	}

	token, err := loadOrCreateTokenAt(TokenPath())
	if err != nil {
		return nil, err
	}
	cliToken, err := loadOrCreateTokenAt(CLITokenPath())
	if err != nil {
		return nil, err
	}

	return &Daemon{cfg: cfg, version: version, logger: logger, mgr: mgr, bus: b, audit: aud,
		vault: v, fleet: fl, plugins: plugins, token: token, cliToken: cliToken}, nil
}

// Run starts the listeners and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	// Refuse to start a second daemon.
	if pingSocket(SocketPath()) {
		return errors.New("a termada daemon is already running")
	}
	_ = os.Remove(SocketPath())

	// Pipe bus events into the audit log.
	ch, cancelSub := d.bus.Subscribe(512)
	go func() {
		for e := range ch {
			d.audit.FromBus(e)
		}
	}()
	defer cancelSub()

	// Notifications (desktop + optional Telegram) on key events.
	notifier := notify.New(d.cfg.Notifications.Desktop, notify.TelegramConfig{
		Enabled:  d.cfg.Notifications.Telegram.Enabled,
		BotToken: d.cfg.Notifications.Telegram.BotToken,
		ChatID:   d.cfg.Notifications.Telegram.ChatID,
	})
	nch, cancelNotify := d.bus.Subscribe(256)
	go notifier.Subscribe(nch)
	defer cancelNotify()

	// Periodically health-check servers so the dashboard shows online/offline
	// without the human clicking.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		time.Sleep(2 * time.Second)
		d.fleet.HealthCheck()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				d.fleet.HealthCheck()
			}
		}
	}()

	// Garbage-collect old terminal jobs from the registry (spec EX-9).
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				d.mgr.GCOnce(3_600_000, 500) // drop terminal jobs >1h old; keep last 500
				d.mgr.ReapOnce()             // SIGKILL runaway/hung jobs (no-op unless max_job_runtime_ms is set)
			}
		}
	}()

	cp := controlplane.New(d.mgr, d.bus, d.audit, d.fleet, d.vault, d.plugins, d.version)
	root := http.NewServeMux()
	root.Handle("/api/", cp.Mux())
	root.Handle("/", dashboard.Handler())

	// Unix socket: local trust, no token.
	uds, err := net.Listen("unix", SocketPath())
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	_ = os.Chmod(SocketPath(), 0o600)
	// The UDS carries BOTH the human CLI and the agent's MCP shim, and an agent
	// can also reach the socket by shelling out (curl --unix-socket …). So the
	// genuinely human-only mutating routes are refused here and served only over
	// the TCP dashboard — otherwise the SEC-7 "an agent cannot change its own
	// policy or add servers" guarantee would be tool-surface-only, not enforced.
	// The human approval routes (approve/deny/stop_all) must STAY reachable here
	// because the CLI uses them over the socket, so they are gated by a CLI auth
	// token instead — a tokenless curl from an agent is refused.
	udsSrv := &http.Server{Handler: udsGuard(d.cliToken, root)}
	go func() { _ = udsSrv.Serve(uds) }()
	d.logger.Printf("control-plane on unix:%s", SocketPath())

	// TCP: dashboard, token + anti-rebinding auth.
	var tcpSrv *http.Server
	if d.cfg.Dashboard.Enabled {
		ln, err := net.Listen("tcp", d.cfg.HTTP.Bind)
		if err != nil {
			// Fatal, not a warning: a failed bind almost always means another
			// daemon already holds the port. Half-running (UDS only, no dashboard)
			// causes confusing split-brain state.
			_ = os.Remove(SocketPath())
			return fmt.Errorf("dashboard bind %s failed (another daemon already running?): %w", d.cfg.HTTP.Bind, err)
		}
		{
			// local-trust defaults ON (absent config field == nil == trusted).
			localTrust := d.cfg.Dashboard.LocalTrust == nil || *d.cfg.Dashboard.LocalTrust
			tcpSrv = &http.Server{Handler: tokenAuth(d.token, localTrust, root)}
			go func() { _ = tcpSrv.Serve(ln) }()
			if localTrust {
				d.logger.Printf("dashboard:  http://%s/   (viewing needs no token on this machine; approving/managing needs it — `termada dashboard` opens the dashboard with the token)", ln.Addr().String())
			} else {
				d.logger.Printf("dashboard:  http://%s/?token=%s", ln.Addr().String(), d.token)
			}
		}
	}

	<-ctx.Done()
	d.logger.Println("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = udsSrv.Shutdown(shutdownCtx)
	if tcpSrv != nil {
		_ = tcpSrv.Shutdown(shutdownCtx)
	}
	d.mgr.Shutdown()
	_ = d.audit.Close()
	_ = os.Remove(SocketPath())
	return nil
}

// humanOnlyRoutes mutate security-sensitive state and are reachable only from
// the TCP dashboard (token / local-trust). The CLI does not use them (it has no
// policy- or server-add commands), so refusing them on the UDS costs nothing and
// closes the agent self-escalation path (an agent could otherwise curl the
// socket to rewrite its own policy or add a server).
var humanOnlyRoutes = map[string]bool{
	"/api/policies/set":    true,
	"/api/policies/remove": true,
	"/api/servers/add":     true,
	"/api/servers/remove":  true,
}

// cliAuthRoutes are the human approval/stop actions the CLI invokes over the UDS
// (`termada approve|deny|stop`). They cannot be refused outright like the routes
// above because the CLI genuinely needs them, but an agent shelling out to curl
// the socket is otherwise indistinguishable from the CLI and could self-approve a
// command it parked under a `confirm` policy. So we require the CLI auth token on
// these routes: the CLI reads it from the 0600 cli.token file and sends it; a
// tokenless agent curl is refused. The TCP dashboard reaches the same handlers
// over its own token/local-trust path, which this guard does not touch.
var cliAuthRoutes = map[string]bool{
	"/api/approve":  true,
	"/api/deny":     true,
	"/api/stop_all": true,
}

// udsGuard wraps the UDS handler: it refuses the human-only mutating routes
// outright and requires the CLI auth token on the human approval routes.
func udsGuard(cliToken string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if humanOnlyRoutes[r.URL.Path] {
			denyUDS(w, "this action is available only from the dashboard, not over the local control socket")
			return
		}
		if cliAuthRoutes[r.URL.Path] {
			got := r.Header.Get("X-Termada-CLI-Token")
			if cliToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(cliToken)) != 1 {
				denyUDS(w, "this action requires the human CLI auth token; agents cannot approve, deny or stop over the local control socket")
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func denyUDS(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"denied_by_policy","message":"` + msg + `"}}`))
}

// sensitiveRoute reports whether a route mutates security-sensitive state and so
// must require the dashboard token on TCP even in local-trust mode — an agent
// runs on the same loopback/uid and would otherwise reach it tokenless. It is the
// union of the routes refused on the UDS and those gated by the CLI token there.
func sensitiveRoute(path string) bool {
	return humanOnlyRoutes[path] || cliAuthRoutes[path]
}

func buildPolicies(cfg config.Config) map[string]policy.Policy {
	out := map[string]policy.Policy{}
	for name, pc := range cfg.Policies {
		aa := make([]policy.AutoAnswer, 0, len(pc.AutoAnswer))
		for _, r := range pc.AutoAnswer {
			aa = append(aa, policy.AutoAnswer{Match: r.Match, Send: r.Send})
		}
		out[name] = policy.Policy{Allow: pc.Allow, Deny: pc.Deny, Confirm: pc.Confirm, AutoAnswer: aa}
	}
	return out
}

func buildAgentPolicies(cfg config.Config) map[string]string {
	out := map[string]string{}
	for _, a := range cfg.Agents {
		out[a.ID] = a.Policy
	}
	return out
}

func buildAgentTokens(cfg config.Config) map[string]string {
	out := map[string]string{}
	for _, a := range cfg.Agents {
		if a.Token != "" {
			out[a.Token] = a.ID
		}
	}
	return out
}

func buildServers(cfg config.Config) []fleet.Server {
	out := make([]fleet.Server, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		out = append(out, fleet.Server{
			Name: s.Name, Host: s.Host, Port: s.Port, User: s.User, Auth: s.Auth, Tags: s.Tags,
		})
	}
	return out
}

func buildRecipes(cfg config.Config) map[string]engine.Recipe {
	out := map[string]engine.Recipe{}
	for name, rc := range cfg.Recipes {
		out[name] = engine.Recipe{Name: name, Target: rc.Target, Steps: rc.Steps}
	}
	return out
}

// buildProtectedPaths lists the paths file_read/file_write must refuse: the
// daemon's own runtime dir (cli.token, dashboard token, vault, audit log,
// registry, known_hosts, snapshots, plugins), the vault file if configured
// elsewhere, the common host credential stores, and any operator additions.
func buildProtectedPaths(cfg config.Config) []string {
	paths := []string{RuntimeDir()}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths,
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".aws"),
			filepath.Join(home, ".gnupg"),
		)
	}
	if cfg.Vault.File != "" {
		paths = append(paths, config.ExpandPath(cfg.Vault.File))
	}
	for _, p := range cfg.Security.ProtectedPaths {
		paths = append(paths, config.ExpandPath(p))
	}
	return paths
}

// resolveSpawn turns the configured security.run_as into a validated spawn
// policy. An empty spec runs agent sessions as the daemon (the default). A
// non-empty spec requires the daemon to be root (we can't setuid otherwise) and
// must not resolve to root — both are fail-closed errors so an operator who
// asked for uid separation never silently gets full-privilege agent sessions.
func resolveSpawn(spec string) (engine.SpawnConfig, error) {
	if strings.TrimSpace(spec) == "" {
		return engine.SpawnConfig{}, nil
	}
	if os.Geteuid() != 0 {
		return engine.SpawnConfig{}, fmt.Errorf("security.run_as=%q requires running the daemon as root (to drop agent sessions to that user); start termada as root or unset run_as", spec)
	}
	uid, gid, err := resolveRunAs(spec)
	if err != nil {
		return engine.SpawnConfig{}, fmt.Errorf("security.run_as: %w", err)
	}
	if uid == 0 {
		return engine.SpawnConfig{}, fmt.Errorf("security.run_as=%q resolves to uid 0 — that defeats the purpose; use a dedicated unprivileged user", spec)
	}
	return engine.SpawnConfig{SeparateUID: true, UID: uid, GID: gid}, nil
}

// resolveRunAs parses a run_as spec: an explicit numeric "uid:gid", a bare
// numeric uid (gid looked up), or a username (uid + primary gid looked up).
func resolveRunAs(spec string) (uid, gid int, err error) {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		u, e1 := strconv.Atoi(strings.TrimSpace(spec[:i]))
		g, e2 := strconv.Atoi(strings.TrimSpace(spec[i+1:]))
		if e1 != nil || e2 != nil {
			return 0, 0, fmt.Errorf("invalid numeric uid:gid %q", spec)
		}
		return u, g, nil
	}
	var usr *user.User
	if _, e := strconv.Atoi(spec); e == nil {
		usr, err = user.LookupId(spec)
	} else {
		usr, err = user.Lookup(spec)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("cannot resolve user %q: %w", spec, err)
	}
	u, e1 := strconv.Atoi(usr.Uid)
	g, e2 := strconv.Atoi(usr.Gid)
	if e1 != nil || e2 != nil {
		return 0, 0, fmt.Errorf("user %q has non-numeric uid/gid", spec)
	}
	return u, g, nil
}

func loadOrCreateTokenAt(p string) (string, error) {
	if b, err := os.ReadFile(p); err == nil && len(b) >= 16 {
		return strings.TrimSpace(string(b)), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	if err := os.WriteFile(p, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// pingSocket reports whether something is already accepting on the socket.
func pingSocket(path string) bool {
	c, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// tokenAuth enforces the dashboard token and guards against DNS-rebinding by
// requiring a loopback Host/Origin (spec M12).
func tokenAuth(token string, localTrust bool, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostLoopback(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !originLoopback(o) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		// The token gates the API (the privileged surface). Static dashboard
		// assets (the SPA, vendored xterm, css) are served freely on loopback.
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" {
			// The Host + Origin checks above already reject every cross-site and
			// DNS-rebinding request, so anything reaching here is a genuine
			// same-machine client. In local-trust mode (the default) that's enough
			// for read/observe routes — no token, so http://127.0.0.1:7717 just
			// works on your own machine.
			//
			// BUT a malicious agent runs on the SAME loopback and uid, so "local =
			// trusted" doesn't hold for the security-sensitive mutating routes
			// (approve/deny/stop_all, policy/server management). Those require the
			// token EVEN in local-trust — otherwise an agent could `curl` the TCP
			// dashboard and self-approve, bypassing the socket guard. The SPA
			// already sends the token (and shows its gate on a 401), so the human
			// dashboard keeps working; only a tokenless caller is refused.
			if !localTrust || sensitiveRoute(r.URL.Path) {
				got := bearer(r)
				if got == "" {
					got = r.URL.Query().Get("token")
				}
				if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
		}
		h.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func hostLoopback(host string) bool {
	h := host
	if x, _, err := net.SplitHostPort(host); err == nil {
		h = x
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
}

func originLoopback(origin string) bool {
	origin = strings.TrimPrefix(origin, "http://")
	origin = strings.TrimPrefix(origin, "https://")
	return hostLoopback(origin)
}
