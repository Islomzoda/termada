// Command termada is the single-binary runtime for AI agents in the terminal.
//
// Phase 1 implements the local persistent-shell execution engine exposed over an
// MCP stdio server. The long-lived daemon, dashboard, SSH and vault are later
// phases (see docs/tz/Termada-TZ.md §30).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/mcp"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
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

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	// stdio is the default (and only) transport in 0.1; the flag is accepted for
	// forward-compatibility and explicitness in MCP client configs.
	_ = fs.Bool("stdio", true, "serve MCP over stdio (default; the only mode in 0.1)")
	daemon := fs.Bool("daemon", false, "run as a long-lived daemon with dashboard (not implemented yet; phase 2)")
	agent := fs.String("agent", "default", "agent id used for attribution")
	_ = fs.Parse(args)

	logger := log.New(os.Stderr, "termada ", log.LstdFlags|log.Lmsgprefix)
	mgr := engine.NewManager(engine.DefaultConfig())
	defer mgr.Shutdown()

	if *daemon {
		logger.Println("daemon mode is not implemented yet (phase 2). Running stdio instead is the default.")
		os.Exit(1)
	}

	srv := mcp.NewServer(mgr, *agent, version, logger)
	logger.Printf("v%s MCP stdio server ready (agent=%s)", version, *agent)
	if err := srv.ServeStdio(os.Stdin, os.Stdout); err != nil {
		logger.Fatalf("serve: %v", err)
	}
	logger.Println("stdin closed, shutting down")
}

func usage() {
	fmt.Fprintf(os.Stderr, `termada %s — reliable, transparent terminal runtime for AI agents

Usage:
  termada serve [--agent <id>]   Run the MCP server over stdio (default mode)
  termada version                Print version
  termada help                   Show this help

MCP client config:
  { "mcpServers": { "termada": { "command": "termada", "args": ["serve"] } } }
`, version)
}
