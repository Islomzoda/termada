// Package daemon is the long-lived termada process (spec R4): it owns the
// engine, event bus, audit log and vault, and serves the control-plane over a
// Unix socket (for the stdio shim/CLI/TUI) and the dashboard over token-gated
// TCP.
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/config"
	"github.com/termada/termada/internal/controlplane"
	"github.com/termada/termada/internal/dashboard"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/ids"
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
	if err := os.Chmod(RuntimeDir(), 0o700); err != nil {
		return nil, fmt.Errorf("secure runtime directory: %w", err)
	}
	b := bus.New(500)
	aud, err := audit.Open(AuditPath(), nil)
	if err != nil {
		return nil, fmt.Errorf("open audit: %w", err)
	}
	var mgr *engine.Manager
	initialized := false
	defer func() {
		if initialized {
			return
		}
		if mgr != nil {
			mgr.Shutdown()
		}
		_ = aud.Close()
	}()

	ec := engine.Config{
		OutputRetentionBytes: cfg.Defaults.OutputRetentionBytes,
		MaxOutputBytes:       cfg.Defaults.MaxOutputBytes,
		PTYCols:              cfg.Defaults.PTYCols,
		MaxForegroundJobs:    cfg.Defaults.MaxForegroundJobs,
		MaxBackgroundJobs:    cfg.Defaults.MaxBackgroundJobs,
		MaxJobsPerAgent:      cfg.Defaults.MaxJobsPerAgent,
		MaxJobRuntimeMS:      cfg.Defaults.MaxJobRuntimeMS,
		DefaultTimeoutMS:     cfg.Defaults.TimeoutMS,
		ConfirmTimeoutMS:     cfg.Defaults.ConfirmTimeoutMS,
		RedactionPatterns:    cfg.Redaction,
	}
	mgr = engine.NewManager(ec)
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
	// enable file_read/file_write against a remote session, over SFTP (binary-safe).
	mgr.SetRemoteFileOps(remoteFileOps{fl: fl, runner: runner})
	// enable port_forward: local→remote SSH tunnels (like `ssh -L`).
	mgr.SetForwardOps(newForwardManager(fl, runner))

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

	d := &Daemon{cfg: cfg, version: version, logger: logger, mgr: mgr, bus: b, audit: aud,
		vault: v, fleet: fl, plugins: plugins, token: token, cliToken: cliToken}
	initialized = true
	return d, nil
}

// Run starts the listeners and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	ownsSocket := false
	cancelAudit := func() {}
	defer func() {
		d.shutdownServices(cancelAudit)
		if ownsSocket {
			_ = os.Remove(SocketPath())
		}
	}()
	unlockDaemon, err := acquireDaemonLock(filepath.Join(RuntimeDir(), "daemon.lock"))
	if err != nil {
		return fmt.Errorf("acquire daemon lock: %w", err)
	}
	defer unlockDaemon()
	// Refuse to start a second daemon.
	if pingSocket(SocketPath()) {
		return errors.New("a termada daemon is already running")
	}
	_ = os.Remove(SocketPath())

	// Audit is a synchronous reliable sink: Publish does not return until the
	// record is appended and fsynced, and delivery errors latch audit unhealthy.
	cancelAudit = d.bus.SubscribeReliable(d.audit.FromBus)

	// Notifications (desktop + optional Telegram) on key events.
	notifier := notify.New(d.cfg.Notifications.Desktop, notify.TelegramConfig{
		Enabled:  d.cfg.Notifications.Telegram.Enabled,
		BotToken: d.cfg.Notifications.Telegram.BotToken,
		ChatID:   d.cfg.Notifications.Telegram.ChatID,
	}, d.mgr.Redactor())
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
				d.mgr.GCOnce(3_600_000, 200) // drop terminal jobs >1h old; keep last 200
				d.mgr.ReapOnce()             // SIGKILL runaway/hung jobs (no-op unless max_job_runtime_ms is set)
				if ms := d.cfg.Vault.IdleRelockMS; ms > 0 {
					d.vault.RelockIfIdle(time.Duration(ms) * time.Millisecond) // auto-lock an idle vault
				}
			}
		}
	}()

	cp := controlplane.New(d.mgr, d.bus, d.audit, d.fleet, d.vault, d.plugins, d.version)
	root := daemonRootHandler(cp.Mux(), dashboard.Handler())

	// Unix socket: agent operations are owner-scoped; operator operations require
	// the separate CLI token.
	uds, err := net.Listen("unix", SocketPath())
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	ownsSocket = true
	if err := os.Chmod(SocketPath(), 0o600); err != nil {
		_ = uds.Close()
		_ = os.Remove(SocketPath())
		return fmt.Errorf("secure control socket: %w", err)
	}
	// An agent can also curl the socket, so socket possession alone never grants
	// the global operator role.
	udsSrv := hardenedHTTPServer(udsGuard(d.cliToken, d.mgr, root))
	go func() { _ = udsSrv.Serve(newLimitedListener(uds, 256)) }()
	d.logger.Printf("control-plane on unix:%s", SocketPath())

	// TCP: dashboard, token + anti-rebinding auth.
	var tcpSrv *http.Server
	if d.cfg.Dashboard.Enabled {
		ln, err := net.Listen("tcp", d.cfg.HTTP.Bind)
		if err != nil {
			// Fatal, not a warning: a failed bind almost always means another
			// daemon already holds the port. Half-running (UDS only, no dashboard)
			// causes confusing split-brain state.
			_ = udsSrv.Close()
			_ = os.Remove(SocketPath())
			return fmt.Errorf("dashboard bind %s failed (another daemon already running?): %w", d.cfg.HTTP.Bind, err)
		}
		{
			tcpSrv = hardenedHTTPServer(tokenAuth(d.token, root))
			cp.SetDashboardURL(dashboardBaseURL(ln.Addr()))
			go func() { _ = tcpSrv.Serve(newLimitedListener(ln, 256)) }()
			dashboardURL := tokenizedDashboardURL(ln.Addr(), d.token)
			d.logger.Printf("dashboard:  %s", dashboardURL)
			if d.cfg.Dashboard.OpenBrowser {
				if err := openDashboardBrowser(dashboardURL); err != nil {
					d.logger.Printf("open dashboard browser: %v", err)
				}
			}
		}
	}

	<-ctx.Done()
	d.logger.Println("shutting down…")
	servers := []*http.Server{udsSrv}
	if tcpSrv != nil {
		servers = append(servers, tcpSrv)
	}
	var shutdownWG sync.WaitGroup
	for _, server := range servers {
		shutdownWG.Add(1)
		go func(server *http.Server) {
			defer shutdownWG.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
		}(server)
	}
	shutdownWG.Wait()
	return nil
}

// shutdownServices keeps the reliable audit sink installed until the engine has
// closed sessions and drained every tracked job watcher. Only then is delivery
// cancelled and the audit file closed.
func (d *Daemon) shutdownServices(cancelAudit func()) {
	d.mgr.Shutdown()
	if cancelAudit != nil {
		cancelAudit()
	}
	_ = d.audit.Close()
}

func daemonRootHandler(controlPlane, dashboardHandler http.Handler) *http.ServeMux {
	root := http.NewServeMux()
	root.Handle("/api/", controlPlane)
	root.Handle("/metrics", controlPlane)
	root.Handle("/", dashboardHandler)
	return root
}

func hardenedHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func newLimitedListener(listener net.Listener, max int) net.Listener {
	return &limitedListener{Listener: listener, slots: make(chan struct{}, max)}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// operatorOnlyRoutes expose global state or mutate operator-managed security
// configuration. UDS callers receive the operator role only by presenting the
// separate CLI token; filesystem access to the socket alone is not sufficient.
var operatorOnlyRoutes = map[string]bool{
	"/api/approve":          true,
	"/api/deny":             true,
	"/api/stop_all":         true,
	"/api/status":           true,
	"/api/pending":          true,
	"/api/audit":            true,
	"/api/events":           true,
	"/api/policies":         true,
	"/api/policies/set":     true,
	"/api/policies/remove":  true,
	"/api/servers/add":      true,
	"/api/servers/remove":   true,
	"/api/servers/test":     true,
	"/api/vault/unlock":     true,
	"/api/vault/status":     true,
	"/api/snapshot/create":  true,
	"/api/snapshot/restore": true,
	"/api/snapshot/list":    true,
	"/api/exec/hold":        true,
	"/api/exec/stream":      true,
	"/api/session/stream":   true,
	"/api/session/write":    true,
	"/metrics":              true,
}

// udsGuard authenticates operator-only UDS routes with the CLI token and injects
// the resulting role into request context. Ordinary agent routes remain open to
// the shim and are identity-scoped by the control-plane server.
func udsGuard(cliToken string, mgr *engine.Manager, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if agentToken := r.Header.Get("X-Termada-Agent-Token"); agentToken != "" {
			if mgr == nil {
				denyUDS(w, "agent token authentication is unavailable")
				return
			}
			if _, err := mgr.AuthenticateAgent(agentToken, ""); err != nil {
				denyUDS(w, "invalid agent token")
				return
			}
		}
		got := r.Header.Get("X-Termada-CLI-Token")
		operator := cliToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(cliToken)) == 1
		// Daemons before /api/ping used GET /api/status as their liveness probe.
		// Preserve that probe for old stdio shims without exposing the operator
		// overview: an authenticated CLI still reaches the full status handler,
		// while anonymous and valid agent callers receive only data-free health.
		if r.Method == http.MethodGet && r.URL.Path == "/api/status" && got == "" && !operator {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{\"ok\":true}\n")
			return
		}
		if operatorOnlyRoutes[r.URL.Path] && !operator {
			denyUDS(w, "this action requires the authenticated human CLI token")
			return
		}
		if operator {
			r = controlplane.WithOperatorPrincipal(r)
		}
		h.ServeHTTP(w, r)
	})
}

func denyUDS(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"denied_by_policy","message":"` + msg + `"}}`))
}

// remoteFileOps implements engine.RemoteFileOps: file_read/file_write against a
// remote session resolve the session's server from the inventory and transfer
// over SFTP (binary-safe). Wired in daemon.New.
type remoteFileOps struct {
	fl     *fleet.Manager
	runner *sshx.Runner
}

func (o remoteFileOps) ReadFile(target, path string, maxBytes int) ([]byte, int64, bool, error) {
	srv, ok := o.fl.Get(target)
	if !ok {
		return nil, 0, false, fmt.Errorf("no server named %q", target)
	}
	return o.runner.SFTPRead(srv, path, maxBytes)
}

func (o remoteFileOps) WriteFile(target, path, content, mode string) (int, error) {
	srv, ok := o.fl.Get(target)
	if !ok {
		return 0, fmt.Errorf("no server named %q", target)
	}
	return o.runner.SFTPWrite(srv, path, content, mode)
}

// forwardManager implements engine.ForwardOps: it keeps a registry of live
// local→remote SSH tunnels. The OS reclaims listeners/sockets on daemon exit, so
// there is no explicit shutdown sweep.
type forwardManager struct {
	fl              *fleet.Manager
	runner          *sshx.Runner
	mu              sync.Mutex
	fwds            map[string]*forwardEntry
	reserved        int
	reservedByOwner map[string]int
	closed          bool
}

type forwardEntry struct {
	info engine.ForwardInfo
	fwd  *sshx.Forward
}

func newForwardManager(fl *fleet.Manager, runner *sshx.Runner) *forwardManager {
	return &forwardManager{fl: fl, runner: runner, fwds: map[string]*forwardEntry{}, reservedByOwner: map[string]int{}}
}

func (m *forwardManager) Start(owner, server, remoteHost string, remotePort int, localBind string) (engine.ForwardInfo, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return engine.ForwardInfo{}, fmt.Errorf("forward manager is shutting down")
	}
	if len(m.fwds)+m.reserved >= 64 || m.countOwnerLocked(owner)+m.reservedByOwner[owner] >= 16 {
		m.mu.Unlock()
		return engine.ForwardInfo{}, fmt.Errorf("port-forward limit reached")
	}
	m.reserved++
	m.reservedByOwner[owner]++
	m.mu.Unlock()
	releaseReservation := func() {
		m.reserved--
		if m.reservedByOwner[owner] <= 1 {
			delete(m.reservedByOwner, owner)
		} else {
			m.reservedByOwner[owner]--
		}
	}
	srv, ok := m.fl.Get(server)
	if !ok {
		m.mu.Lock()
		releaseReservation()
		m.mu.Unlock()
		return engine.ForwardInfo{}, fmt.Errorf("no server named %q", server)
	}
	f, err := m.runner.OpenForward(srv, localBind, remoteHost, remotePort)
	if err != nil {
		m.mu.Lock()
		releaseReservation()
		m.mu.Unlock()
		return engine.ForwardInfo{}, err
	}
	info := engine.ForwardInfo{ID: ids.New("fwd"), Server: server, RemoteHost: remoteHost, RemotePort: remotePort, LocalAddr: f.Addr(), Owner: owner}
	m.mu.Lock()
	releaseReservation()
	if m.closed {
		m.mu.Unlock()
		_ = f.Close()
		return engine.ForwardInfo{}, fmt.Errorf("forward manager is shutting down")
	}
	m.fwds[info.ID] = &forwardEntry{info: info, fwd: f}
	m.mu.Unlock()
	return info, nil
}

func (m *forwardManager) countOwnerLocked(owner string) int {
	count := 0
	for _, entry := range m.fwds {
		if entry.info.Owner == owner {
			count++
		}
	}
	return count
}

func (m *forwardManager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	entries := make([]*forwardEntry, 0, len(m.fwds))
	for _, entry := range m.fwds {
		entries = append(entries, entry)
	}
	m.fwds = map[string]*forwardEntry{}
	m.mu.Unlock()
	for _, entry := range entries {
		_ = entry.fwd.Close()
	}
}

func (m *forwardManager) List(owner string) []engine.ForwardInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]engine.ForwardInfo, 0, len(m.fwds))
	for _, e := range m.fwds {
		if owner != "" && e.info.Owner != owner {
			continue
		}
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *forwardManager) Close(owner, id string) error {
	m.mu.Lock()
	e := m.fwds[id]
	if e != nil && owner != "" && e.info.Owner != owner {
		e = nil
	}
	if e != nil {
		delete(m.fwds, id)
	}
	m.mu.Unlock()
	if e == nil {
		return fmt.Errorf("forward %q not found", id)
	}
	return e.fwd.Close()
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
	username := strconv.Itoa(uid)
	home := "/"
	if usr, lookupErr := user.LookupId(strconv.Itoa(uid)); lookupErr == nil {
		if usr.Username != "" {
			username = usr.Username
		}
		if filepath.IsAbs(usr.HomeDir) {
			home = usr.HomeDir
		}
	}
	return engine.SpawnConfig{SeparateUID: true, UID: uid, GID: gid, Username: username, HomeDir: home}, nil
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
		if err := validateRunAsIDs(u, g); err != nil {
			return 0, 0, err
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
	if err := validateRunAsIDs(u, g); err != nil {
		return 0, 0, fmt.Errorf("user %q: %w", spec, err)
	}
	return u, g, nil
}

// uid_t/gid_t are uint32 on the supported Unix targets. Refuse root identities,
// negative values, and the all-ones sentinel before the later syscall.Credential
// conversion; otherwise a large int such as 2^32 would wrap to root.
func validateRunAsIDs(uid, gid int) error {
	const maxCredentialID = uint64(1<<32 - 2)
	if uid <= 0 || uint64(uid) > maxCredentialID {
		return fmt.Errorf("uid %d is outside the unprivileged credential range 1..%d", uid, maxCredentialID)
	}
	if gid <= 0 || uint64(gid) > maxCredentialID {
		return fmt.Errorf("gid %d is outside the unprivileged credential range 1..%d", gid, maxCredentialID)
	}
	return nil
}

func loadOrCreateTokenAt(p string) (string, error) {
	readExisting := func() (string, error) {
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("token file %s is not a regular file", p)
		}
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		defer f.Close()
		actual, err := f.Stat()
		if err != nil || !actual.Mode().IsRegular() || !os.SameFile(fi, actual) {
			return "", fmt.Errorf("token file %s changed while opening", p)
		}
		if fi.Mode().Perm() != 0o600 {
			if err := f.Chmod(0o600); err != nil {
				return "", fmt.Errorf("secure token file %s: %w", p, err)
			}
		}
		b, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil {
			return "", err
		}
		if len(b) > 4096 {
			return "", fmt.Errorf("token file %s exceeds 4096 byte limit", p)
		}
		tok := strings.TrimSpace(string(b))
		if len(tok) < 16 {
			return "", fmt.Errorf("token file %s is empty or too short", p)
		}
		for _, r := range tok {
			if r < 0x21 || r > 0x7e {
				return "", fmt.Errorf("token file %s contains invalid characters", p)
			}
		}
		return tok, nil
	}
	if tok, err := readExisting(); err == nil {
		return tok, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return readExisting()
		}
		return "", err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(p)
		}
	}()
	if _, err := f.WriteString(tok); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
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

// tokenAuth enforces the dashboard token for the complete TCP API. A spoofed
// Host header can never grant an API principal, including when the listener is
// bound to a wildcard address.
func tokenAuth(token string, h http.Handler) http.Handler {
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
			got := bearer(r)
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			r = controlplane.WithOperatorPrincipal(r)
		}
		h.ServeHTTP(w, r)
	})
}

func tokenizedDashboardURL(addr net.Addr, token string) string {
	return dashboardBaseURL(addr) + "?token=" + url.QueryEscape(token)
}

func dashboardBaseURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		host, port = "127.0.0.1", "7717"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func openDashboardBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
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
