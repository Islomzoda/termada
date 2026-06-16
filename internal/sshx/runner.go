// Package sshx is the SSH execution backend for fleet runs (spec §14). It dials
// a server using credentials from the vault and runs a command, with TOFU
// host-key verification (spec RM-6: trust on first use, reject on mismatch).
//
// NOTE: the live SSH path requires a reachable server and is exercised by
// integration tests where one is available; the fleet selection/aggregation
// logic is unit-tested independently via a mock Runner.
package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/vault"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Runner runs commands over SSH. It satisfies fleet.Runner.
type Runner struct {
	vault      *vault.Vault
	knownHosts string
	timeout    time.Duration
	keyDir     string // default on-disk key dir; "" => ~/.ssh (overridable for tests)
}

// NewRunner builds an SSH runner.
func NewRunner(v *vault.Vault, knownHostsPath string, timeout time.Duration) *Runner {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Runner{vault: v, knownHosts: knownHostsPath, timeout: timeout}
}

// Run executes command on server and returns a structured result.
func (r *Runner) Run(server fleet.Server, command []string) fleet.Result {
	start := time.Now()
	res := fleet.Result{Server: server.Name}
	client, err := r.dial(server)
	if err != nil {
		res.Status = classifyDialErr(err)
		res.Error = err.Error()
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		res.Status = "conn_lost"
		res.Error = err.Error()
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	runErr := sess.Run(shellJoin(command))
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	res.DurationMS = time.Since(start).Milliseconds()

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
	port := server.Port
	if port == 0 {
		port = 22
	}
	auths, err := r.authMethods(server)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            server.User,
		Auth:            auths,
		HostKeyCallback: r.hostKeyCallback(),
		Timeout:         r.timeout,
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", server.Host, port), cfg)
}

// authMethods decides how to authenticate to server:
//   - server.Auth set   -> the stored vault credential (private key or password);
//     this needs the vault unlocked.
//   - server.Auth empty -> the operator's own SSH identity: ssh-agent plus the
//     default ~/.ssh keys. So a server you can already `ssh` into needs no stored
//     secret — and therefore no vault and no passphrase (spec RM: agent auth).
func (r *Runner) authMethods(server fleet.Server) ([]ssh.AuthMethod, error) {
	if server.Auth != "" {
		if r.vault == nil || r.vault.Locked() {
			return nil, fmt.Errorf("vault is locked; run `termada unlock`")
		}
		secret, ok := r.vault.Get(server.Auth)
		if !ok {
			return nil, fmt.Errorf("no vault entry %q for server %s", server.Auth, server.Name)
		}
		if strings.Contains(secret, "PRIVATE KEY") {
			signer, err := ssh.ParsePrivateKey([]byte(secret))
			if err != nil {
				return nil, fmt.Errorf("parse private key: %w", err)
			}
			return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
		}
		return []ssh.AuthMethod{ssh.Password(secret)}, nil
	}
	var methods []ssh.AuthMethod
	if m := agentAuth(); m != nil {
		methods = append(methods, m)
	}
	if signers := defaultKeySigners(r.keyDir); len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no credential for %s: store one (add an SSH key/password), or set up ssh-agent or a key in ~/.ssh", server.Name)
	}
	return methods, nil
}

// agentAuth offers the keys held by a running ssh-agent ($SSH_AUTH_SOCK). nil if
// no agent is reachable.
func agentAuth() ssh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers)
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
		_ = os.MkdirAll(filepath.Dir(r.knownHosts), 0o700)
		check, err := knownhosts.New(r.knownHosts)
		if err != nil {
			// No known_hosts yet: trust on first use.
			return appendKnownHost(r.knownHosts, hostname, key)
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
	_, err = f.WriteString(line + "\n")
	return err
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
	if s == "" {
		return "''"
	}
	safe := true
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("_@%+=:,./-", c)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
