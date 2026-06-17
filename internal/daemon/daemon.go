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
	"path/filepath"
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

// AuditPath is the audit log file.
func AuditPath() string { return filepath.Join(RuntimeDir(), "audit.log") }

// Daemon bundles the running services.
type Daemon struct {
	cfg     config.Config
	version string
	logger  *log.Logger
	mgr     *engine.Manager
	bus     *bus.Bus
	audit   *audit.Logger
	vault   *vault.Vault
	fleet   *fleet.Manager
	plugins *plugin.Manager
	token   string
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
			return nil, fmt.Errorf("no server named %q", target)
		}
		return runner.OpenShell(srv, cols, rows)
	})

	plugins := plugin.New(filepath.Join(RuntimeDir(), "plugins"))
	if err := plugins.Load(); err != nil {
		logger.Printf("warning: plugin load: %v", err)
	}

	token, err := loadOrCreateToken()
	if err != nil {
		return nil, err
	}

	return &Daemon{cfg: cfg, version: version, logger: logger, mgr: mgr, bus: b, audit: aud,
		vault: v, fleet: fl, plugins: plugins, token: token}, nil
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
	udsSrv := &http.Server{Handler: dashboardOnly(root)}
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
				d.logger.Printf("dashboard:  http://%s/   (open on this machine — no token needed; `termada dashboard` opens it)", ln.Addr().String())
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

// dashboardOnly wraps the UDS handler and refuses the human-only mutating routes.
func dashboardOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if humanOnlyRoutes[r.URL.Path] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"denied_by_policy","message":"this action is available only from the dashboard, not over the local control socket"}}`))
			return
		}
		h.ServeHTTP(w, r)
	})
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

func loadOrCreateToken() (string, error) {
	p := TokenPath()
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
			// same-machine client. In local-trust mode (the default) that's
			// enough — no token, so http://127.0.0.1:7717 just works on your own
			// machine. The token is still accepted, and REQUIRED when local-trust
			// is off (shared / multi-user hosts).
			if !localTrust {
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
