// Command termada is the single-binary runtime for AI agents in the terminal.
//
//	termada serve            run the long-lived daemon (control-plane + dashboard)
//	termada serve --stdio    run the MCP stdio shim (what an MCP client launches)
//	termada status|jobs|...  inspect and control a running daemon
//	termada vault ...        manage the encrypted credential store
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/config"
	"github.com/termada/termada/internal/controlplane"
	"github.com/termada/termada/internal/daemon"
	"github.com/termada/termada/internal/mcp"
	"github.com/termada/termada/internal/selfupdate"
	"github.com/termada/termada/internal/tui"
	"github.com/termada/termada/internal/vault"
	"golang.org/x/term"
)

// version is overridable via -ldflags "-X main.version=..." at release build time
// (goreleaser injects the git tag); the constant here is the dev-build fallback.
var version = "0.11.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "status":
		cmdStatus()
	case "jobs":
		cmdJobs(os.Args[2:])
	case "sessions":
		cmdSessions()
	case "logs":
		cmdLogs(os.Args[2:])
	case "kill":
		cmdKill(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "pending":
		cmdPending()
	case "approve":
		cmdResolve("approve", os.Args[2:])
	case "deny":
		cmdResolve("deny", os.Args[2:])
	case "audit":
		cmdAudit(os.Args[2:])
	case "top", "watch":
		cmdTop()
	case "vault":
		cmdVault(os.Args[2:])
	case "unlock":
		cmdUnlock()
	case "servers":
		cmdServers()
	case "dashboard", "ui":
		cmdDashboard(os.Args[2:])
	case "open":
		cmdDashboard(append([]string{"--open"}, os.Args[2:]...))
	case "snapshot":
		cmdSnapshot(os.Args[2:])
	case "setup":
		cmdSetup()
	case "doctor":
		cmdDoctor()
	case "service":
		cmdService(os.Args[2:])
	case "update":
		cmdUpdate()
	case "sign-checksums": // hidden release tool: sign a file with $TERMADA_RELEASE_PRIVKEY → <file>.sig
		cmdSignChecksums(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("termada", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// ---- serve (daemon + shim) ----

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	stdio := fs.Bool("stdio", false, "run the MCP stdio shim (what an MCP client launches)")
	agent := fs.String("agent", "default", "agent id used for attribution")
	token := fs.String("token", os.Getenv("TERMADA_AGENT_TOKEN"), "per-agent identity token (or $TERMADA_AGENT_TOKEN); makes agent_id non-spoofable when configured")
	cfgPath := fs.String("config", config.DefaultPath(), "config file path")
	bind := fs.String("bind", os.Getenv("TERMADA_BIND"), "dashboard bind address (or $TERMADA_BIND); e.g. 0.0.0.0:7717 in a container — map it to host loopback (-p 127.0.0.1:7717:7717)")
	_ = fs.Parse(args)
	if *stdio {
		if err := runShim(*agent, *token); err != nil {
			fatal(err)
		}
		return
	}
	runDaemon(*cfgPath, *bind)
}

func runDaemon(cfgPath, bind string) {
	logger := log.New(os.Stderr, "termada ", log.LstdFlags|log.Lmsgprefix)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	// An explicit --bind / $TERMADA_BIND overrides the config — needed in a
	// container, where the default 127.0.0.1 bind is unreachable from the host.
	if bind != "" {
		cfg.HTTP.Bind = bind
	}
	d, err := daemon.New(cfg, version, logger)
	if err != nil {
		logger.Fatalf("daemon: %v", err)
	}
	logger.Printf("termada v%s daemon starting", version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil {
		logger.Fatalf("run: %v", err)
	}
}

func runShim(agent, token string) error {
	logger := log.New(os.Stderr, "termada-shim ", log.LstdFlags|log.Lmsgprefix)
	client := controlplane.NewUnixClient(daemon.SocketPath())
	client.SetToken(token)

	if client.Ping() == nil {
		logger.Printf("connected to daemon (agent=%s)", agent)
	} else if spawnDaemon(logger) && waitDaemon(client) {
		logger.Printf("started daemon; connected (agent=%s)", agent)
	} else {
		return fmt.Errorf("termada daemon is unavailable and could not be started; refusing insecure in-process fallback (see %s)", filepath.Join(daemon.RuntimeDir(), "daemon.log"))
	}
	srv := mcp.NewServer(client, agent, version, logger)
	return srv.ServeStdio(os.Stdin, os.Stdout)
}

func spawnDaemon(logger *log.Logger) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	_ = os.MkdirAll(daemon.RuntimeDir(), 0o700)
	logf, _ := os.OpenFile(filepath.Join(daemon.RuntimeDir(), "daemon.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if logf != nil {
		defer logf.Close()
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = detachAttr()
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logger.Printf("spawn daemon: %v", err)
		return false
	}
	if err := cmd.Process.Release(); err != nil {
		logger.Printf("release daemon process handle: %v", err)
	}
	return true
}

func waitDaemon(client *controlplane.Client) bool {
	for i := 0; i < 40; i++ {
		if client.Ping() == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// ---- inspection / control (control-plane clients) ----

func mustClient() *controlplane.Client {
	c := controlplane.NewUnixClient(daemon.SocketPath())
	// The human approval routes (approve/deny/stop) are gated on the socket by the
	// CLI auth token so an agent curling the socket can't self-approve. As the
	// human CLI we can read the 0600 token file the daemon wrote; present it.
	if b, err := os.ReadFile(daemon.CLITokenPath()); err == nil {
		c.SetCLIToken(strings.TrimSpace(string(b)))
	}
	if err := c.Ping(); err != nil {
		fmt.Fprintln(os.Stderr, "no termada daemon running. Start one with: termada serve")
		os.Exit(1)
	}
	return c
}

func cmdStatus() {
	c := mustClient()
	s, err := c.Status()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("termada v%s\n", s.Version)
	fmt.Printf("sessions: %d   active jobs: %d   pending approvals: %d\n",
		len(s.Sessions), len(s.Jobs), len(s.Pending))
	for _, j := range s.Jobs {
		fmt.Printf("  %s  %-18s  %s\n", j.JobID, j.Status, strings.Join(j.Command, " "))
	}
	for _, p := range s.Pending {
		fmt.Printf("  ⚠ %s  needs approval: %s\n", p.ConfirmationID, strings.Join(p.Command, " "))
	}
}

func cmdJobs(args []string) {
	fs := flag.NewFlagSet("jobs", flag.ExitOnError)
	follow := fs.Bool("f", false, "follow")
	filter := fs.String("filter", "all", "active|recent|all")
	_ = fs.Parse(args)
	c := mustClient()
	for {
		jobs := c.ListJobs("", *filter) // human CLI: unscoped, sees every agent's jobs
		fmt.Print("\033[H\033[2J")
		for _, j := range jobs {
			fmt.Printf("%s  %-18s  %s\n", j.JobID, j.Status, strings.Join(j.Command, " "))
		}
		if !*follow {
			return
		}
		time.Sleep(time.Second)
	}
}

func cmdSessions() {
	c := mustClient()
	for _, s := range c.ListSessions("") {
		fmt.Printf("%s  owner=%s  %s  active=%d\n", s.SessionID, s.Owner, s.Target, s.ActiveJobs)
	}
}

func cmdLogs(args []string) {
	// Accept both `logs -f <job>` and the documented `logs <job> -f`. The
	// standard flag package stops at the first positional argument.
	jobID, follow, err := parseLogsArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c := mustClient()
	cursor := ""
	for {
		res, err := c.Tail("", jobID, cursor) // human CLI: unscoped
		if err != nil {
			fatal(err)
		}
		fmt.Print(res.Lines)
		cursor = res.NextCursor
		if res.HasMore {
			continue
		}
		if !follow {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func parseLogsArgs(args []string) (jobID string, follow bool, err error) {
	positional := make([]string, 0, 1)
	for _, arg := range args {
		switch arg {
		case "-f", "--follow":
			follow = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown logs option: %s", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) != 1 {
		return "", false, fmt.Errorf("usage: termada logs <job_id> [-f]")
	}
	return positional[0], follow, nil
}

func cmdKill(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: termada kill <job_id>")
		os.Exit(2)
	}
	c := mustClient()
	if err := c.Kill("", args[0]); err != nil { // human CLI: unscoped
		fatal(err)
	}
	fmt.Println("killed", args[0])
}

func cmdStop(args []string) {
	c := mustClient()
	n, err := c.StopAll()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("stopped %d job(s)\n", n)
}

func cmdPending() {
	c := mustClient()
	p, err := c.Pending()
	if err != nil {
		fatal(err)
	}
	if len(p) == 0 {
		fmt.Println("no pending approvals")
		return
	}
	for _, x := range p {
		fmt.Printf("%s  agent=%s  matched=%s  %s\n", x.ConfirmationID, x.AgentID, x.Matched, strings.Join(x.Command, " "))
	}
}

func cmdResolve(kind string, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "usage: termada %s <confirmation_id>\n", kind)
		os.Exit(2)
	}
	c := mustClient()
	var err error
	if kind == "approve" {
		err = c.Approve(args[0], "cli")
	} else {
		err = c.Deny(args[0], "cli")
	}
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%sd %s\n", kind, args[0])
}

func cmdAudit(args []string) {
	if len(args) > 0 && args[0] == "verify" {
		n, err := audit.VerifyAll(daemon.AuditPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "audit chain INVALID: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("audit chain OK: %d records verified\n", n)
		return
	}
	c := mustClient()
	recs, err := c.AuditTail(100)
	if err != nil {
		fatal(err)
	}
	for _, r := range recs {
		fmt.Printf("%v  %-18v  %v  %v\n", r["time"], r["type"], r["agent_id"], r["message"])
	}
}

func cmdTop() {
	c := mustClient()
	if err := tui.Run(c); err != nil {
		fatal(err)
	}
}

// ---- vault ----

func cmdVault(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: termada vault <init|set|list|rm|reset> ...")
		os.Exit(2)
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		fatal(fmt.Errorf("config: %w", err))
	}
	v := vault.New(config.ExpandPath(cfg.Vault.File))
	switch args[0] {
	case "init":
		if v.Exists() {
			fmt.Fprintln(os.Stderr, "vault already exists")
			os.Exit(1)
		}
		pass := readPassphrase("Set vault passphrase: ")
		again := readPassphrase("Confirm passphrase: ")
		if pass != again {
			fmt.Fprintln(os.Stderr, "passphrases do not match")
			os.Exit(1)
		}
		if err := v.Init(pass); err != nil {
			fatal(err)
		}
		fmt.Println("vault created at", v.PathString())
	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: termada vault set <name>")
			os.Exit(2)
		}
		unlock(v)
		val := readPassphrase("Secret value: ")
		if err := v.Set(args[1], val); err != nil {
			fatal(err)
		}
		fmt.Println("stored", args[1])
	case "list":
		unlock(v)
		for _, n := range v.List() {
			fmt.Println(n)
		}
	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: termada vault rm <name>")
			os.Exit(2)
		}
		unlock(v)
		if err := v.Delete(args[1]); err != nil {
			fatal(err)
		}
		fmt.Println("removed", args[1])
	case "reset":
		// Escape hatch for a forgotten passphrase: back up the vault and clear the
		// path so a fresh passphrase can be set (its contents stay recoverable in
		// the .bak only if the old passphrase is remembered).
		if !v.Exists() {
			fmt.Println("no vault to reset")
			return
		}
		p := v.PathString()
		fmt.Printf("Reset the vault at %s?\nStored credentials become unrecoverable without the OLD passphrase.\nType 'reset' to confirm: ", p)
		var confirm string
		_, _ = fmt.Scanln(&confirm)
		if confirm != "reset" {
			fmt.Println("aborted")
			return
		}
		bak := fmt.Sprintf("%s.bak-%d", p, time.Now().Unix())
		if err := os.Rename(p, bak); err != nil {
			fatal(err)
		}
		fmt.Printf("vault moved to %s\nset a new passphrase by adding a server in the dashboard, or `termada vault init`\n", bak)
	default:
		fmt.Fprintln(os.Stderr, "unknown vault subcommand:", args[0])
		os.Exit(2)
	}
}

func cmdUnlock() {
	c := mustClient()
	pass := readPassphrase("Vault passphrase: ")
	n, err := c.Unlock(pass)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("vault unlocked (%d secrets available to the daemon)\n", n)
}

func cmdServers() {
	c := mustClient()
	servers := c.ServerList()
	if len(servers) == 0 {
		fmt.Println("no servers configured")
		return
	}
	for _, s := range servers {
		fmt.Printf("%-16s %s@%s  %v\n", s.Name, s.User, s.Host, s.Tags)
	}
}

// cmdService installs/uninstalls termada as a per-user background service so the
// daemon starts on login and stays running (launchd on macOS, systemd --user on
// Linux) — no more "nothing works because the daemon isn't up".
func cmdService(args []string) {
	action := "install"
	if len(args) > 0 {
		action = args[0]
	}
	switch runtime.GOOS {
	case "darwin":
		serviceDarwin(action)
	case "linux":
		serviceLinux(action)
	default:
		fmt.Fprintln(os.Stderr, "service is supported on macOS (launchd) and Linux (systemd --user)")
		os.Exit(2)
	}
}

func serviceDarwin(action string) {
	label := "com.termada.daemon"
	plist := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", label+".plist")
	logf := filepath.Join(daemon.RuntimeDir(), "daemon.log")
	switch action {
	case "install":
		body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>serve</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, label, selfPath(), logf, logf)
		_ = os.MkdirAll(filepath.Dir(plist), 0o755)
		_ = os.MkdirAll(daemon.RuntimeDir(), 0o700)
		if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
			fatal(err)
		}
		_ = exec.Command("launchctl", "unload", plist).Run()
		if err := exec.Command("launchctl", "load", "-w", plist).Run(); err != nil {
			fatal(fmt.Errorf("launchctl load: %v (a daemon may already hold the port — `termada stop` first)", err))
		}
		fmt.Printf("✓ installed launchd agent %s\n  plist: %s\n  logs:  %s\ntermada now starts on login and restarts if it crashes.\n", label, plist, logf)
	case "uninstall":
		_ = exec.Command("launchctl", "unload", plist).Run()
		_ = os.Remove(plist)
		fmt.Println("✓ uninstalled launchd agent", label)
	case "status":
		out, err := exec.Command("launchctl", "list", label).CombinedOutput()
		if err != nil {
			fmt.Println("not loaded — install with: termada service install")
			return
		}
		fmt.Print(string(out))
	default:
		fmt.Fprintln(os.Stderr, "usage: termada service <install|uninstall|status>")
		os.Exit(2)
	}
}

func serviceLinux(action string) {
	unit := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", "termada.service")
	sc := func(a ...string) error { return exec.Command("systemctl", append([]string{"--user"}, a...)...).Run() }
	switch action {
	case "install":
		body := fmt.Sprintf(`[Unit]
Description=termada daemon (control-plane + dashboard)
After=network.target

[Service]
ExecStart=%s serve
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, selfPath())
		_ = os.MkdirAll(filepath.Dir(unit), 0o755)
		if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
			fatal(err)
		}
		_ = sc("daemon-reload")
		if err := sc("enable", "--now", "termada.service"); err != nil {
			fatal(fmt.Errorf("systemctl --user enable --now termada: %v (try `loginctl enable-linger %s` for boot-time start)", err, os.Getenv("USER")))
		}
		fmt.Printf("✓ installed systemd --user unit\n  unit: %s\ntermada now starts with your session and restarts on failure.\n  (for start at boot without login: loginctl enable-linger %s)\n", unit, os.Getenv("USER"))
	case "uninstall":
		_ = sc("disable", "--now", "termada.service")
		_ = os.Remove(unit)
		_ = sc("daemon-reload")
		fmt.Println("✓ uninstalled systemd --user unit termada.service")
	case "status":
		_ = exec.Command("systemctl", "--user", "status", "termada.service").Run()
	default:
		fmt.Fprintln(os.Stderr, "usage: termada service <install|uninstall|status>")
		os.Exit(2)
	}
}

// cmdDoctor diagnoses a setup in one command: what's working, what isn't, and the
// exact fix for each — so a newcomer never gets silently stuck.
func cmdDoctor() {
	ok := func(s string) { fmt.Printf("  \033[32m✓\033[0m %s\n", s) }
	bad := func(s, hint string) { fmt.Printf("  \033[31m✗\033[0m %s\n      → %s\n", s, hint) }
	warn := func(s, hint string) { fmt.Printf("  \033[33m!\033[0m %s\n      → %s\n", s, hint) }

	fmt.Printf("termada doctor — v%s\n\n", version)

	self := selfPath()
	dir := filepath.Dir(self)
	onPath := false
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			onPath = true
			break
		}
	}
	if onPath {
		ok("binary on PATH: " + self)
	} else {
		warn("binary not on PATH ("+self+")", "add: export PATH=\""+dir+":$PATH\"")
	}

	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		warn("config "+config.DefaultPath()+": "+err.Error(), "using defaults; check the YAML")
	} else {
		ok("config: " + config.DefaultPath())
	}

	if fi, e := os.Stat(daemon.RuntimeDir()); e == nil && fi.IsDir() {
		ok("runtime dir: " + daemon.RuntimeDir())
	} else {
		warn("runtime dir missing: "+daemon.RuntimeDir(), "run: termada setup")
	}

	c := controlplane.NewUnixClient(daemon.SocketPath())
	dashboardBind := cfg.HTTP.Bind
	if dashboardBind == "" {
		dashboardBind = "127.0.0.1:7717"
	}
	dashboardDisabled := !cfg.Dashboard.Enabled
	if b, err := os.ReadFile(daemon.CLITokenPath()); err == nil {
		c.SetCLIToken(strings.TrimSpace(string(b)))
	}
	if e := c.Ping(); e != nil {
		bad("daemon not running", "start it: termada serve   (or: termada service install)")
	} else {
		ok("daemon running (socket: " + daemon.SocketPath() + ")")
		if s, e := c.Status(); e == nil {
			ok(fmt.Sprintf("control-plane OK: v%s, %d session(s), %d active job(s), %d pending approval(s)",
				s.Version, len(s.Sessions), len(s.Jobs), len(s.Pending)))
			dashboardDisabled = s.DashboardURL == ""
			if u, err := url.Parse(s.DashboardURL); err == nil && u.Host != "" {
				dashboardBind = u.Host
			}
		}
	}

	if dashboardDisabled {
		warn("dashboard disabled", "set dashboard.enabled: true and restart the daemon")
	} else if conn, e := net.DialTimeout("tcp", dashboardBind, time.Second); e == nil {
		_ = conn.Close()
		ok("dashboard port " + dashboardBind + " reachable (termada dashboard to open)")
	} else {
		warn("dashboard port "+dashboardBind+" not listening", "the daemon binds it — start: termada serve")
	}

	if cfg.Vault.File == "" {
		cfg.Vault.File = "~/.config/termada/vault.age"
	}
	if vault.New(config.ExpandPath(cfg.Vault.File)).Exists() {
		ok("vault file present (unlock state lives in the daemon — see the dashboard)")
	} else {
		warn("no vault yet", "optional: only needed to STORE creds; ssh-agent/~/.ssh servers need none")
	}

	if os.Getenv("SSH_AUTH_SOCK") != "" {
		ok("ssh-agent available — remote servers can use your keys (no vault needed)")
	} else {
		warn("no ssh-agent ($SSH_AUTH_SOCK unset)", "fine if you use ~/.ssh keys or store creds in the vault")
	}

	if _, e := exec.LookPath("claude"); e == nil {
		ok("claude CLI found — register: claude mcp add --scope user termada -- termada serve --stdio")
	} else {
		warn("claude CLI not found", "install Claude Code, or add termada to your MCP client's config")
	}
	fmt.Println()
}

// cmdDashboard prints the tokenized dashboard URL (and optionally opens it), so a
// user who lost the link from the `serve` output has a one-command way back in.
func cmdDashboard(args []string) {
	open := false
	for _, a := range args {
		if a == "--open" || a == "-o" || a == "open" {
			open = true
		}
	}
	status, err := mustClient().Status()
	if err != nil {
		fatal(err)
	}
	base := status.DashboardURL
	if base == "" {
		fatal(fmt.Errorf("the running daemon is not serving a dashboard; enable dashboard.enabled and restart it"))
	}
	// The complete TCP API requires the dashboard token. The SPA stores it in
	// sessionStorage and removes it from the address bar after bootstrap.
	dashboardURL := base
	if tok, err := os.ReadFile(daemon.TokenPath()); err == nil {
		if t := strings.TrimSpace(string(tok)); t != "" {
			dashboardURL, err = withDashboardToken(base, t)
			if err != nil {
				fatal(err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "dashboard token at %s is empty — restart the daemon to repair it\n", daemon.TokenPath())
			os.Exit(1)
		}
	} else {
		fmt.Fprintf(os.Stderr, "no dashboard token at %s — is the daemon running?\nstart it with:  termada serve\n", daemon.TokenPath())
		os.Exit(1)
	}
	fmt.Println(dashboardURL)
	if open {
		if err := openURL(dashboardURL); err != nil {
			fmt.Fprintln(os.Stderr, "could not open a browser:", err)
		}
	}
}

func withDashboardToken(base, token string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid dashboard URL %q", base)
	}
	query := u.Query()
	query.Set("token", token)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// openURL launches the default browser at url (best-effort, per-OS).
func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func cmdSnapshot(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: termada snapshot <create <path>|list|restore <id>>")
		os.Exit(2)
	}
	c := mustClient()
	switch args[0] {
	case "create":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: termada snapshot create <path>")
			os.Exit(2)
		}
		abs, _ := filepath.Abs(args[1])
		snap, err := c.SnapshotCreate(abs)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("snapshot %s  (%d bytes)  %s\n", snap.ID, snap.Bytes, snap.Source)
	case "list":
		snaps, err := c.SnapshotList()
		if err != nil {
			fatal(err)
		}
		for _, s := range snaps {
			fmt.Printf("%s  %d bytes  %s\n", s.ID, s.Bytes, s.Source)
		}
	case "restore":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: termada snapshot restore <id>")
			os.Exit(2)
		}
		if err := c.SnapshotRestore(args[1]); err != nil {
			fatal(err)
		}
		fmt.Println("restored", args[1])
	default:
		fmt.Fprintln(os.Stderr, "unknown snapshot subcommand:", args[0])
		os.Exit(2)
	}
}

func cmdUpdate() {
	exe, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	fmt.Fprintln(os.Stderr, "checking github.com/Islomzoda/termada for updates…")
	tag, err := selfupdate.Run("Islomzoda/termada", version, exe)
	if err != nil {
		fatal(err)
	}
	if strings.TrimPrefix(tag, "v") == version {
		fmt.Printf("already up to date (v%s)\n", version)
		return
	}
	fmt.Printf("updated to %s — restart termada to apply\n", tag)
}

// cmdSignChecksums is the release-side counterpart to the in-binary signature
// check: it signs <file> with the ed25519 private key in $TERMADA_RELEASE_PRIVKEY
// (base64) and writes <file>.sig. Hidden (not in usage) — used by goreleaser.
func cmdSignChecksums(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: termada sign-checksums <file>  (signs with $TERMADA_RELEASE_PRIVKEY)"))
	}
	priv := os.Getenv("TERMADA_RELEASE_PRIVKEY")
	pub := os.Getenv("TERMADA_RELEASE_PUBKEY")
	if err := signChecksumsFile(args[0], priv, pub); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s.sig\n", args[0])
}

func signChecksumsFile(path, priv, pub string) error {
	if (priv == "") != (pub == "") {
		return fmt.Errorf("TERMADA_RELEASE_PRIVKEY and TERMADA_RELEASE_PUBKEY must be set together")
	}
	if priv == "" {
		return fmt.Errorf("TERMADA_RELEASE_PRIVKEY and TERMADA_RELEASE_PUBKEY are required to sign checksums")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sig, err := selfupdate.SignEd25519(data, priv)
	if err != nil {
		return err
	}
	if err := selfupdate.VerifyEd25519(data, sig, pub); err != nil {
		return fmt.Errorf("release signing key pair does not match: %w", err)
	}
	return os.WriteFile(path+".sig", []byte(sig), 0o644)
}

func cmdSetup() {
	_ = os.MkdirAll(daemon.RuntimeDir(), 0o700)
	self := selfPath()
	fmt.Printf("termada setup\n  runtime: %s\n  config:  %s\n\n", daemon.RuntimeDir(), config.DefaultPath())

	// Actually register with Claude Code if its CLI is present (idempotent-ish).
	if _, err := exec.LookPath("claude"); err == nil {
		out, err := exec.Command("claude", "mcp", "add", "--scope", "user", "termada", "--", self, "serve", "--stdio").CombinedOutput()
		s := strings.ToLower(string(out))
		switch {
		case err == nil:
			fmt.Println("✓ registered the MCP server with Claude Code (termada)")
		case strings.Contains(s, "already") || strings.Contains(s, "exists"):
			fmt.Println("✓ already registered with Claude Code")
		default:
			fmt.Printf("! couldn't auto-register (%v) — run it yourself:\n    claude mcp add --scope user termada -- %s serve --stdio\n", err, self)
		}
	} else {
		fmt.Printf("• register with your MCP client (Claude Code):\n    claude mcp add --scope user termada -- %s serve --stdio\n", self)
	}

	fmt.Printf(`
Next:
  termada service install   # run the daemon in the background (start on login)
      …or: termada serve     # run it in the foreground
  termada dashboard --open   # open the dashboard
  termada doctor             # check the local runtime and daemon
`)
}

func unlock(v *vault.Vault) {
	if !v.Exists() {
		fmt.Fprintln(os.Stderr, "no vault. Create one with: termada vault init")
		os.Exit(1)
	}
	pass := readPassphrase("Vault passphrase: ")
	if err := v.Unlock(pass); err != nil {
		fatal(err)
	}
}

func readPassphrase(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fatal(err)
	}
	return strings.TrimRight(string(b), "\r\n")
}

func selfPath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "termada"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprintf(os.Stderr, `termada %s — reliable, transparent terminal runtime for AI agents

Getting started:
  termada setup                 register with Claude Code + next steps
  termada service install       run the daemon in the background (start on login)
  termada dashboard [--open]    print/open the dashboard URL
  termada doctor                check the local runtime and daemon

Daemon & agent:
  termada serve                 run the daemon (control-plane + dashboard)
  termada serve --stdio         run the MCP stdio shim (launched by MCP clients)

Inspect & control:
  termada status                overview of agents, sessions, jobs, approvals
  termada jobs [-f]             list jobs
  termada sessions              list sessions
  termada logs <job_id> [-f]    stream a job's output
  termada kill <job_id>         kill a job
  termada stop                  kill-switch: stop all active jobs
  termada pending               list commands awaiting approval
  termada approve <id>          approve a pending command
  termada deny <id>             deny a pending command
  termada audit [verify]        show audit feed / verify the tamper-evident chain
  termada top                   live TUI

Credentials:
  termada vault init|set|list|rm|reset

MCP client config:
  { "mcpServers": { "termada": { "command": "termada", "args": ["serve","--stdio"] } } }
`, version)
}
