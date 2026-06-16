// Package tui is the terminal observability view (`termada top`, spec §8.2). It
// renders the same live state as the dashboard for people who prefer the
// terminal. This 0.x version is a periodic full-screen refresh; a richer
// bubbletea-based interactive view is a later iteration.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/termada/termada/internal/controlplane"
)

// Run renders a live status view until interrupted (Ctrl-C).
func Run(c *controlplane.Client) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Print("\033[?25l")       // hide cursor
	defer fmt.Print("\033[?25h") // restore cursor

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	render(c)
	for {
		select {
		case <-ctx.Done():
			fmt.Print("\033[2J\033[H")
			return nil
		case <-ticker.C:
			render(c)
		}
	}
}

func render(c *controlplane.Client) {
	s, err := c.Status()
	fmt.Print("\033[2J\033[H") // clear + home
	fmt.Printf("\033[1m▦ TERMADA top\033[0m   %s\n", time.Now().Format("15:04:05"))
	if err != nil {
		fmt.Printf("\n  daemon unreachable: %v\n", err)
		return
	}
	fmt.Printf("  v%s   sessions:%d   active jobs:%d   pending:%d\n",
		s.Version, len(s.Sessions), len(s.Jobs), len(s.Pending))

	if len(s.Pending) > 0 {
		fmt.Printf("\n\033[33m  PENDING APPROVAL\033[0m  (termada approve <id>)\n")
		for _, p := range s.Pending {
			fmt.Printf("  ⚠ %s  %s  %s\n", short(p.ConfirmationID), p.AgentID, strings.Join(p.Command, " "))
		}
	}

	fmt.Printf("\n  %-20s %-18s %s\n", "JOB", "STATUS", "COMMAND")
	if len(s.Jobs) == 0 {
		fmt.Println("  (no active jobs)")
	}
	for _, j := range s.Jobs {
		fmt.Printf("  %-20s %s%-18s\033[0m %s\n", short(j.JobID), color(string(j.Status)), j.Status, strings.Join(j.Command, " "))
	}

	fmt.Printf("\n  SESSIONS\n")
	for _, ss := range s.Sessions {
		fmt.Printf("  %-20s owner=%-10s active=%d\n", short(ss.SessionID), ss.Owner, ss.ActiveJobs)
	}
	fmt.Print("\n  \033[2mCtrl-C to quit\033[0m\n")
	_ = os.Stdout.Sync()
}

func short(id string) string {
	if len(id) > 18 {
		return id[:18]
	}
	return id
}

func color(status string) string {
	switch status {
	case "running":
		return "\033[34m"
	case "exited":
		return "\033[32m"
	case "killed", "failed", "timed_out":
		return "\033[31m"
	case "awaiting_input", "awaiting_confirmation":
		return "\033[33m"
	default:
		return ""
	}
}
