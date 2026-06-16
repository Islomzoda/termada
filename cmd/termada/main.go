// Command termada is the single-binary runtime for AI agents in the terminal.
//
//	termada serve            run the long-lived daemon (control-plane + dashboard)
//	termada serve --stdio    run the MCP stdio shim (what an MCP client launches)
//	termada status|jobs|...  inspect and control a running daemon
//	termada vault ...        manage the encrypted credential store
//
// See docs/tz/Termada-TZ.md for the full spec.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/config"
	"github.com/termada/termada/internal/controlplane"
	"github.com/termada/termada/internal/daemon"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/mcp"
	"github.com/termada/termada/internal/selfupdate"
	"github.com/termada/termada/internal/tui"
	"github.com/termada/termada/internal/vault"
	"golang.org/x/term"
)

const version = "0.5.0"

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
	case "snapshot":
		cmdSnapshot(os.Args[2:])
	case "setup":
		cmdSetup()
	case "update":
		cmdUpdate()
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
	cfgPath := fs.String("config", config.DefaultPath(), "config file path")
	_ = fs.Parse(args)
	if *stdio {
		runShim(*agent)
		return
	}
	runDaemon(*cfgPath)
}

func runDaemon(cfgPath string) {
	logger := log.New(os.Stderr, "termada ", log.LstdFlags|log.Lmsgprefix)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Fatalf("config: %v", err)
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

func runShim(agent string) {
	logger := log.New(os.Stderr, "termada-shim ", log.LstdFlags|log.Lmsgprefix)
	client := controlplane.NewUnixClient(daemon.SocketPath())
	var backend mcp.Backend
	var cleanup func()

	if client.Ping() == nil {
		backend = client
		logger.Printf("connected to daemon (agent=%s)", agent)
	} else if spawnDaemon(logger) && waitDaemon(client) {
		backend = client
		logger.Printf("started daemon; connected (agent=%s)", agent)
	} else {
		logger.Printf("no daemon available — running in-process (dashboard disabled)")
		mgr := engine.NewManager(engine.DefaultConfig())
		backend = mcp.NewLocalBackend(mgr)
		cleanup = mgr.Shutdown
	}
	srv := mcp.NewServer(backend, agent, version, logger)
	if err := srv.ServeStdio(os.Stdin, os.Stdout); err != nil {
		logger.Printf("serve: %v", err)
	}
	if cleanup != nil {
		cleanup()
	}
}

func spawnDaemon(logger *log.Logger) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	_ = os.MkdirAll(daemon.RuntimeDir(), 0o700)
	logf, _ := os.OpenFile(filepath.Join(daemon.RuntimeDir(), "daemon.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = detachAttr()
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logger.Printf("spawn daemon: %v", err)
		return false
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
		jobs := c.ListJobs(*filter)
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
	for _, s := range c.ListSessions() {
		fmt.Printf("%s  owner=%s  %s  active=%d\n", s.SessionID, s.Owner, s.Target, s.ActiveJobs)
	}
}

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "follow")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: termada logs <job_id> [-f]")
		os.Exit(2)
	}
	c := mustClient()
	jobID, cursor := fs.Arg(0), ""
	for {
		res, err := c.Tail(jobID, cursor)
		if err != nil {
			fatal(err)
		}
		fmt.Print(res.Lines)
		cursor = res.NextCursor
		if !*follow {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func cmdKill(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: termada kill <job_id>")
		os.Exit(2)
	}
	c := mustClient()
	if err := c.Kill(args[0]); err != nil {
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
		n, err := audit.Verify(daemon.AuditPath())
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
		fmt.Fprintln(os.Stderr, "usage: termada vault <init|set|list|rm> ...")
		os.Exit(2)
	}
	cfg, _ := config.Load(config.DefaultPath())
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

func cmdSetup() {
	_ = os.MkdirAll(daemon.RuntimeDir(), 0o700)
	fmt.Printf(`termada setup

Runtime dir: %s
Config:      %s

1. (optional) create an encrypted vault for credentials:
     termada vault init
2. start the daemon (control-plane + dashboard):
     termada serve
3. register the MCP shim with your client:
     claude mcp add termada -- %s serve --stdio
`, daemon.RuntimeDir(), config.DefaultPath(), selfPath())
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
  termada vault init|set|list|rm
  termada setup                 first-run guidance

MCP client config:
  { "mcpServers": { "termada": { "command": "termada", "args": ["serve","--stdio"] } } }
`, version)
}
