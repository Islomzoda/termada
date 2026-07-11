// Package sshx is the SSH execution backend for fleet runs (spec §14). It dials
// a server using credentials from the vault and runs a command, with TOFU
// host-key verification (spec RM-6: trust on first use, reject on mismatch).
//
// NOTE: the live SSH path requires a reachable server and is exercised by
// integration tests where one is available; the fleet selection/aggregation
// logic is unit-tested independently via a mock Runner.
package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/vault"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// defaultCmdTimeout bounds a single fleet command so a hung remote process can't
// pin a parallelism slot and stall fleet_run forever. Generous (fleet commands
// are short ops, not dev servers) but finite. Overridable via SetCommandTimeout.
const defaultCmdTimeout = 10 * time.Minute

// defaultKeepalive pings a persistent remote shell so a silently-dropped link (no
// TCP RST — e.g. a NAT/firewall timeout or a frozen host) is detected and the
// session reconnects, instead of a Read hanging forever. Overridable for tests.
const defaultKeepalive = 15 * time.Second

// defaultIOTimeout bounds SSH channel setup and SFTP operations. The command
// runtime has its own, longer limit; this covers protocol requests that would
// otherwise wait forever on a connected but unresponsive peer.
const defaultIOTimeout = 30 * time.Second

// maxCommandOutputBytes bounds each output stream returned by one fleet SSH
// command. Fleet results are retained and serialized as a whole, so an
// untrusted remote must not be able to grow daemon memory without bound.
const maxCommandOutputBytes = 1 << 20

const outputTruncatedMarker = "\n[termada: output truncated]\n"

// knownHostsMu serializes the read-check-append TOFU transaction. Without it,
// simultaneous first connections could both trust different keys for the same
// host before either append became visible.
var knownHostsMu sync.Mutex

type cappedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	stored := 0
	if remaining := b.limit - len(b.data); remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		b.data = append(b.data, p[:remaining]...)
		stored = remaining
	}
	if stored < len(p) {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	if b.truncated {
		return string(b.data) + outputTruncatedMarker
	}
	return string(b.data)
}

// Runner runs commands over SSH. It satisfies fleet.Runner.
type Runner struct {
	vault      *vault.Vault
	knownHosts string
	timeout    time.Duration // dial timeout
	cmdTimeout time.Duration // per-command execution timeout (0 = no cap)
	ioTimeout  time.Duration // channel setup, interactive writes and SFTP operations
	keepalive  time.Duration // persistent-shell keepalive interval (0 = off)
	keyDir     string        // default on-disk key dir; "" => ~/.ssh (overridable for tests)
	sftpSlots  chan struct{}
}

// NewRunner builds an SSH runner.
func NewRunner(v *vault.Vault, knownHostsPath string, timeout time.Duration) *Runner {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Runner{vault: v, knownHosts: knownHostsPath, timeout: timeout, cmdTimeout: defaultCmdTimeout, ioTimeout: defaultIOTimeout, keepalive: defaultKeepalive, sftpSlots: make(chan struct{}, 16)}
}

// SetCommandTimeout overrides the per-command execution timeout (0 disables it).
func (r *Runner) SetCommandTimeout(d time.Duration) { r.cmdTimeout = d }

// SetIOTimeout overrides the SSH channel/SFTP operation timeout (0 disables it).
func (r *Runner) SetIOTimeout(d time.Duration) { r.ioTimeout = d }

// SetKeepalive overrides the persistent-shell keepalive interval (0 disables it).
func (r *Runner) SetKeepalive(d time.Duration) { r.keepalive = d }

// runWithTimeout runs fn, returning its error, or a timeout error if it does not
// finish within d (then onTimeout fires, e.g. to close the session). d <= 0 runs
// fn synchronously with no cap. The bool reports whether it timed out.
func runWithTimeout(d time.Duration, fn func() error, onTimeout func()) (error, bool) {
	if d <= 0 {
		return fn(), false
	}
	done := make(chan error, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case err := <-done:
		return err, false
	case <-timer.C:
		if onTimeout != nil {
			onTimeout()
		}
		return fmt.Errorf("command timed out after %s", d), true
	}
}

// Run executes command on server and returns a structured result.
func (r *Runner) Run(server fleet.Server, command []string) fleet.Result {
	start := time.Now()
	res := fleet.Result{Server: server.Name}
	client, transport, err := r.dialTransport(server)
	if err != nil {
		res.Status = classifyDialErr(err)
		res.Error = err.Error()
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer client.Close()
	if err := setDeadline(transport, r.ioTimeout); err != nil {
		res.Status = "conn_lost"
		res.Error = fmt.Sprintf("set SSH setup deadline: %v", err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	sess, err := client.NewSession()
	if err != nil {
		res.Status = "conn_lost"
		res.Error = err.Error()
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer sess.Close()
	if err := transport.SetDeadline(time.Time{}); err != nil {
		res.Status = "conn_lost"
		res.Error = fmt.Sprintf("clear SSH setup deadline: %v", err)
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}

	stdout := newCappedBuffer(maxCommandOutputBytes)
	stderr := newCappedBuffer(maxCommandOutputBytes)
	sess.Stdout = stdout
	sess.Stderr = stderr
	runErr, timedOut := runWithTimeout(r.cmdTimeout, func() error {
		return sess.Run(shellJoin(command))
	}, func() { _ = transport.Close() })
	res.DurationMS = time.Since(start).Milliseconds()
	if timedOut {
		// Don't read the output buffers here: the abandoned sess.Run goroutine may
		// still be writing to them until Close takes effect (a data race). Report
		// the timeout without partial output.
		res.Status = "timeout"
		res.Error = runErr.Error()
		return res
	}
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()

	if runErr == nil {
		res.Status = "ok"
		return res
	}
	var ee *ssh.ExitError
	if errors.As(runErr, &ee) {
		res.Status = "nonzero_exit"
		res.ExitCode = ee.ExitStatus()
		return res
	}
	res.Status = "conn_lost"
	res.Error = runErr.Error()
	return res
}

func (r *Runner) dial(server fleet.Server) (*ssh.Client, error) {
	client, _, err := r.dialTransport(server)
	return client, err
}

// dialTransport retains the underlying net.Conn so callers can apply operation
// deadlines or forcibly unblock an SSH channel request on timeout.
func (r *Runner) dialTransport(server fleet.Server) (*ssh.Client, net.Conn, error) {
	port := server.Port
	if port == 0 {
		port = 22
	}
	auths, cleanup, err := r.authMethods(server)
	if err != nil {
		return nil, nil, err
	}
	// The ssh-agent socket is only needed during the handshake; close it once
	// Dial returns so a per-dial agent connection can't leak (a 30s fleet
	// health-loop would otherwise exhaust FDs over time, taking down all dials).
	defer cleanup()
	cfg := &ssh.ClientConfig{
		User:            server.User,
		Auth:            auths,
		HostKeyCallback: r.hostKeyCallback(),
		Timeout:         r.timeout,
	}
	addr := sshServerAddress(server.Host, port)
	transport, err := (&net.Dialer{Timeout: r.timeout}).Dial("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	if err := setDeadline(transport, r.timeout); err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	conn, chans, reqs, err := ssh.NewClientConn(transport, addr, cfg)
	if err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	if err := transport.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return ssh.NewClient(conn, chans, reqs), transport, nil
}

func setDeadline(conn net.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		return conn.SetDeadline(time.Time{})
	}
	return conn.SetDeadline(time.Now().Add(timeout))
}

func sshServerAddress(host string, port int) string {
	// Inventory values normally store a raw IPv6 literal, but tolerate brackets
	// copied from a URL/config example before applying JoinHostPort.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

// authMethods decides how to authenticate to server:
//   - server.Auth set   -> the stored vault credential (private key or password);
//     this needs the vault unlocked.
//   - server.Auth empty -> the operator's own SSH identity: ssh-agent plus the
//     default ~/.ssh keys. So a server you can already `ssh` into needs no stored
//     secret — and therefore no vault and no passphrase (spec RM: agent auth).
//
// It also returns a cleanup func the caller MUST invoke after the handshake to
// release any ssh-agent connection opened for agent auth.
func (r *Runner) authMethods(server fleet.Server) ([]ssh.AuthMethod, func(), error) {
	noop := func() {}
	if server.Auth != "" {
		if r.vault == nil || r.vault.Locked() {
			return nil, noop, fmt.Errorf("vault is locked; run `termada unlock`")
		}
		secret, ok := r.vault.Get(server.Auth)
		if !ok {
			return nil, noop, fmt.Errorf("no vault entry %q for server %s", server.Auth, server.Name)
		}
		if strings.Contains(secret, "PRIVATE KEY") {
			signer, err := ssh.ParsePrivateKey([]byte(secret))
			if err != nil {
				return nil, noop, fmt.Errorf("parse private key: %w", err)
			}
			return []ssh.AuthMethod{ssh.PublicKeys(signer)}, noop, nil
		}
		return []ssh.AuthMethod{ssh.Password(secret)}, noop, nil
	}
	var methods []ssh.AuthMethod
	var conns []net.Conn
	if m, conn := agentAuth(r.timeout); m != nil {
		methods = append(methods, m)
		if conn != nil {
			conns = append(conns, conn)
		}
	}
	if signers := defaultKeySigners(r.keyDir); len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if len(methods) == 0 {
		return nil, noop, fmt.Errorf("no credential for %s: store one (add an SSH key/password), or set up ssh-agent or a key in ~/.ssh", server.Name)
	}
	cleanup := func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}
	return methods, cleanup, nil
}

// agentAuth offers the keys held by a running ssh-agent ($SSH_AUTH_SOCK). It
// returns the underlying connection so the caller can close it after the
// handshake; both are nil if no agent is reachable.
func agentAuth(timeout time.Duration) (ssh.AuthMethod, net.Conn) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil
	}
	conn, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return nil, nil
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), conn
}

// defaultKeySigners loads the usual on-disk private keys. dir defaults to ~/.ssh.
// Encrypted keys (which would need a passphrase we can't prompt for) fail to
// parse and are skipped — use ssh-agent for those.
func defaultKeySigners(dir string) []ssh.Signer {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, ".ssh")
	}
	var signers []ssh.Signer
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if s, err := ssh.ParsePrivateKey(b); err == nil {
			signers = append(signers, s)
		}
	}
	return signers
}

// hostKeyCallback implements TOFU: unknown hosts are recorded on first use,
// known hosts must match, mismatches are rejected.
func (r *Runner) hostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()

		if err := os.MkdirAll(filepath.Dir(r.knownHosts), 0o700); err != nil {
			return fmt.Errorf("create known_hosts directory: %w", err)
		}
		unlock, err := lockKnownHosts(r.knownHosts + ".lock")
		if err != nil {
			return fmt.Errorf("lock known_hosts: %w", err)
		}
		defer unlock()

		check, err := knownhosts.New(r.knownHosts)
		if err != nil {
			if os.IsNotExist(err) {
				// No known_hosts yet: trust on first use.
				return appendKnownHost(r.knownHosts, hostname, key)
			}
			// A malformed/unreadable database is not equivalent to first use. Accepting
			// here would silently discard the operator's existing trust decisions.
			return fmt.Errorf("load known_hosts %s: %w", r.knownHosts, err)
		}
		e := check(hostname, remote, key)
		if e == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if errors.As(e, &ke) && len(ke.Want) == 0 {
			return appendKnownHost(r.knownHosts, hostname, key)
		}
		return fmt.Errorf("host key mismatch for %s (possible MITM): %w", hostname, e)
	}
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	if _, err = f.WriteString(line + "\n"); err != nil {
		return err
	}
	return f.Sync()
}

func classifyDialErr(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	msg := err.Error()
	if strings.Contains(msg, "unable to authenticate") || strings.Contains(msg, "permission denied") {
		return "denied"
	}
	return "unreachable"
}

func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
