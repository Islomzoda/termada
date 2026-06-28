package sshx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/vault"
	"golang.org/x/crypto/ssh"
)

// startTestSSHServer runs a minimal in-process SSH server that accepts a
// password and executes "exec" requests via /bin/sh, returning its address.
func startTestSSHServer(t *testing.T, password string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == password {
				return nil, nil
			}
			return nil, errAuth
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, cfg)
		}
	}()
	return ln.Addr().String()
}

// startTestSSHServerPubkey accepts exactly the given public key (no password).
func startTestSSHServerPubkey(t *testing.T, authorized ssh.PublicKey) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	want := authorized.Marshal()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), want) {
				return nil, nil
			}
			return nil, errAuth
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, cfg)
		}
	}()
	return ln.Addr().String()
}

var errAuth = &sshError{"password rejected"}

type sshError struct{ s string }

func (e *sshError) Error() string { return e.s }

func serveConn(nConn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range requests {
				switch req.Type {
				case "pty-req":
					_ = req.Reply(true, nil)
				case "shell":
					// interactive shell on a PTY (for persistent remote sessions)
					_ = req.Reply(true, nil)
					f, err := pty.Start(exec.Command("/bin/bash"))
					if err != nil {
						_ = ch.Close()
						return
					}
					go func() { _, _ = io.Copy(f, ch) }() // channel -> pty
					go func() { _, _ = io.Copy(ch, f) }() // pty -> channel
					return
				case "exec":
					var payload struct{ Command string }
					_ = ssh.Unmarshal(req.Payload, &payload)
					_ = req.Reply(true, nil)
					cmd := exec.Command("/bin/sh", "-c", payload.Command)
					cmd.Stdout = ch
					cmd.Stderr = ch.Stderr()
					code := 0
					if err := cmd.Run(); err != nil {
						if ee, ok := err.(*exec.ExitError); ok {
							code = ee.ExitCode()
						} else {
							code = 1
						}
					}
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
					_ = ch.Close()
					return
				default:
					_ = req.Reply(false, nil)
				}
			}
		}()
	}
}

func newTestRunner(t *testing.T, password string) *Runner {
	t.Helper()
	dir := t.TempDir()
	v := vault.New(filepath.Join(dir, "vault.age"))
	if err := v.Init("vaultpass"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("test-cred", password); err != nil {
		t.Fatal(err)
	}
	return NewRunner(v, filepath.Join(dir, "known_hosts"), 5*time.Second)
}

func serverAt(t *testing.T, addr string) fleet.Server {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return fleet.Server{Name: "test", Host: host, Port: port, User: "tester", Auth: "test-cred"}
}

func TestSSHRunCommand(t *testing.T) {
	addr := startTestSSHServer(t, "sshpass")
	r := newTestRunner(t, "sshpass")
	res := r.Run(serverAt(t, addr), []string{"echo", "hello over ssh"})
	if res.Status != "ok" {
		t.Fatalf("status = %s (err=%s)", res.Status, res.Error)
	}
	if !strings.Contains(res.Stdout, "hello over ssh") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestRunWithTimeout(t *testing.T) {
	if err, to := runWithTimeout(50*time.Millisecond, func() error { return nil }, nil); err != nil || to {
		t.Fatalf("fast fn: err=%v timedOut=%v", err, to)
	}
	closed := false
	_, to := runWithTimeout(20*time.Millisecond, func() error { time.Sleep(time.Second); return nil }, func() { closed = true })
	if !to {
		t.Fatal("slow fn did not time out")
	}
	if !closed {
		t.Fatal("onTimeout was not called")
	}
	if _, to := runWithTimeout(0, func() error { return nil }, nil); to {
		t.Fatal("disabled (d=0) reported a timeout")
	}
}

// A hung remote command is cut off at the per-command timeout instead of pinning
// a parallelism slot forever — exercised end-to-end against the in-process SSH
// server.
func TestSSHCommandTimeout(t *testing.T) {
	addr := startTestSSHServer(t, "sshpass")
	r := newTestRunner(t, "sshpass")
	r.SetCommandTimeout(300 * time.Millisecond)
	start := time.Now()
	res := r.Run(serverAt(t, addr), []string{"sleep", "5"})
	if res.Status != "timeout" {
		t.Fatalf("status = %s (err=%s), want timeout", res.Status, res.Error)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v, expected ~300ms", elapsed)
	}
}

func TestSSHExitCode(t *testing.T) {
	addr := startTestSSHServer(t, "sshpass")
	r := newTestRunner(t, "sshpass")
	res := r.Run(serverAt(t, addr), []string{"sh", "-c", "exit 3"})
	if res.Status != "nonzero_exit" || res.ExitCode != 3 {
		t.Fatalf("status=%s exit=%d, want nonzero_exit/3", res.Status, res.ExitCode)
	}
}

func TestSSHAuthDenied(t *testing.T) {
	addr := startTestSSHServer(t, "correct-pass")
	r := newTestRunner(t, "wrong-pass")
	res := r.Run(serverAt(t, addr), []string{"echo", "x"})
	if res.Status != "denied" {
		t.Fatalf("status = %s, want denied", res.Status)
	}
}

func TestSSHHostKeyTOFU(t *testing.T) {
	addr := startTestSSHServer(t, "sshpass")
	r := newTestRunner(t, "sshpass") // fresh known_hosts -> trust on first use
	if res := r.Run(serverAt(t, addr), []string{"true"}); res.Status != "ok" {
		t.Fatalf("first connect: %s", res.Status)
	}
	// Second connect must succeed against the now-known host key.
	if res := r.Run(serverAt(t, addr), []string{"true"}); res.Status != "ok" {
		t.Fatalf("second connect (known host): %s", res.Status)
	}
}

func TestSSHViaFleet(t *testing.T) {
	addr := startTestSSHServer(t, "sshpass")
	r := newTestRunner(t, "sshpass")
	srv := serverAt(t, addr)
	m := fleet.New([]fleet.Server{srv}, r, 2)
	res, err := m.Run([]string{"echo", "fleet-ssh"}, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || len(res.Results) != 1 || res.Results[0].Status != "ok" {
		t.Fatalf("fleet result = %+v", res)
	}
}

// A server with no stored credential (Auth=="") connects using the operator's
// own default ~/.ssh key — no vault, no passphrase.
func TestSSHDefaultKeyNoVault(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "") // isolate from any real agent — exercise the on-disk key path
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519"), pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}

	addr := startTestSSHServerPubkey(t, signer.PublicKey())
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	// nil vault — proving agent/default-key auth needs no vault at all.
	r := NewRunner(nil, filepath.Join(t.TempDir(), "known_hosts"), 5*time.Second)
	r.keyDir = dir
	res := r.Run(fleet.Server{Name: "keyhost", Host: host, Port: port, User: "tester", Auth: ""},
		[]string{"echo", "via-default-key"})
	if res.Status != "ok" {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	if !strings.Contains(res.Stdout, "via-default-key") {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestRemoteSessionOverSSH(t *testing.T) {
	addr := startTestSSHServer(t, "sshpass")
	r := newTestRunner(t, "sshpass")
	srv := serverAt(t, addr)

	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetRemoteDialer(func(target string, cols, rows int) (engine.ShellConn, error) {
		return r.OpenShell(srv, cols, rows)
	})

	// open a PERSISTENT remote session (shell over SSH) and run two commands;
	// cwd must persist between them just like a local session.
	sess, err := m.CreateSession("agent", "remote", "shell")
	if err != nil {
		t.Fatalf("create remote session: %v", err)
	}
	wait := func(j *engine.Job) {
		select {
		case <-j.Done():
		case <-time.After(8 * time.Second):
			t.Fatalf("remote job did not finish (status=%v)", j.Snapshot().Status)
		}
	}
	j1, err := m.Start("agent", sess.ID, []string{"cd", "/tmp"}, "")
	if err != nil {
		t.Fatalf("remote cd: %v", err)
	}
	wait(j1)
	j2, err := m.Start("agent", sess.ID, []string{"pwd"}, "")
	if err != nil {
		t.Fatalf("remote pwd: %v", err)
	}
	wait(j2)
	res, err := m.Poll(j2.ID, "")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.Contains(res.StdoutChunk, "tmp") {
		t.Fatalf("remote pwd = %q, want it to contain tmp (cwd persisted over SSH)", res.StdoutChunk)
	}
}
