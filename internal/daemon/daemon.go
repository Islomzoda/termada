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
	"github.com/termada/termada/internal/policy"
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
		DefaultTimeoutMS:     cfg.Defaults.TimeoutMS,
		ConfirmTimeoutMS:     cfg.Defaults.ConfirmTimeoutMS,
		RedactionPatterns:    cfg.Redaction,
	}
	mgr := engine.NewManager(ec)
	mgr.SetBus(b)
	mgr.SetPolicy(policy.NewEngine(buildPolicies(cfg)), buildAgentPolicies(cfg))

	// audit uses the same redactor as the engine so secrets are masked in the log.
	aud.SetRedactor(mgr.Redactor())

	token, err := loadOrCreateToken()
	if err != nil {
		return nil, err
	}

	return &Daemon{cfg: cfg, version: version, logger: logger, mgr: mgr, bus: b, audit: aud, token: token}, nil
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

	cp := controlplane.New(d.mgr, d.bus, d.audit, d.version)
	root := http.NewServeMux()
	root.Handle("/api/", cp.Mux())
	root.Handle("/", dashboard.Handler())

	// Unix socket: local trust, no token.
	uds, err := net.Listen("unix", SocketPath())
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	_ = os.Chmod(SocketPath(), 0o600)
	udsSrv := &http.Server{Handler: root}
	go func() { _ = udsSrv.Serve(uds) }()
	d.logger.Printf("control-plane on unix:%s", SocketPath())

	// TCP: dashboard, token + anti-rebinding auth.
	var tcpSrv *http.Server
	if d.cfg.Dashboard.Enabled {
		ln, err := net.Listen("tcp", d.cfg.HTTP.Bind)
		if err != nil {
			d.logger.Printf("warning: dashboard tcp listen failed: %v", err)
		} else {
			tcpSrv = &http.Server{Handler: tokenAuth(d.token, root)}
			go func() { _ = tcpSrv.Serve(ln) }()
			d.logger.Printf("dashboard:  http://%s/?token=%s", ln.Addr().String(), d.token)
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
		got := bearer(r)
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
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
