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

// TestIntegrationRealSSH exercises the runner against a REAL OpenSSH server
// (e.g. a docker sshd container), not the in-process harness. Opt-in:
//
//	TERMADA_IT_SSH=127.0.0.1:2222 TERMADA_IT_SSH_USER=root \
//	TERMADA_IT_SSH_PASS=testpass go test ./internal/sshx/ -run IntegrationRealSSH
func TestIntegrationRealSSH(t *testing.T) {
	addr := os.Getenv("TERMADA_IT_SSH")
	if addr == "" {
		t.Skip("set TERMADA_IT_SSH=host:port (+ _USER/_PASS) to run against a real sshd")
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	dir := t.TempDir()
	v := vault.New(filepath.Join(dir, "v.age"))
	if err := v.Init("vaultpass"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("cred", os.Getenv("TERMADA_IT_SSH_PASS")); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(v, filepath.Join(dir, "known_hosts"), 8*time.Second)
	srv := fleet.Server{Name: "it", Host: host, Port: port, User: os.Getenv("TERMADA_IT_SSH_USER"), Auth: "cred"}

	res := r.Run(srv, []string{"echo", "real-ssh-ok"})
	if res.Status != "ok" || !strings.Contains(res.Stdout, "real-ssh-ok") {
		t.Fatalf("exec: status=%s out=%q err=%s", res.Status, res.Stdout, res.Error)
	}
	if res2 := r.Run(srv, []string{"sh", "-c", "exit 7"}); res2.Status != "nonzero_exit" || res2.ExitCode != 7 {
		t.Fatalf("exit code: status=%s code=%d", res2.Status, res2.ExitCode)
	}
	r.SetCommandTimeout(400 * time.Millisecond)
	start := time.Now()
	if res3 := r.Run(srv, []string{"sleep", "10"}); res3.Status != "timeout" {
		t.Fatalf("timeout: status=%s err=%s", res3.Status, res3.Error)
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Fatalf("timeout took %v against real ssh", el)
	}
}

// TestIntegrationKeepaliveDropDetected freezes the SSH server (docker pause) so
// the link goes silent with no RST, and verifies keepalive detects it and closes
// the connection (a subsequent write fails) instead of hanging forever. Opt-in:
//
//	TERMADA_IT_SSH=127.0.0.1:2222 TERMADA_IT_SSH_USER=root TERMADA_IT_SSH_PASS=testpass \
//	TERMADA_IT_DOCKER=tsshd go test ./internal/sshx/ -run IntegrationKeepalive
func TestIntegrationKeepaliveDropDetected(t *testing.T) {
	addr, cname := os.Getenv("TERMADA_IT_SSH"), os.Getenv("TERMADA_IT_DOCKER")
	if addr == "" || cname == "" {
		t.Skip("set TERMADA_IT_SSH and TERMADA_IT_DOCKER (container name) to run")
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	dir := t.TempDir()
	v := vault.New(filepath.Join(dir, "v.age"))
	_ = v.Init("vaultpass")
	_ = v.Set("cred", os.Getenv("TERMADA_IT_SSH_PASS"))
	r := NewRunner(v, filepath.Join(dir, "known_hosts"), 8*time.Second)
	r.SetKeepalive(time.Second)
	srv := fleet.Server{Name: "it", Host: host, Port: port, User: os.Getenv("TERMADA_IT_SSH_USER"), Auth: "cred"}

	shell, err := r.OpenShell(srv, 80, 24)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer shell.Close()

	_ = exec.Command("docker", "pause", cname).Run()
	defer exec.Command("docker", "unpause", cname).Run()
	time.Sleep(4 * time.Second) // > keepalive interval + ping timeout

	if _, err := shell.Write([]byte("echo hi\n")); err == nil {
		t.Fatal("write succeeded after a silent drop — keepalive did not detect it")
	}
}

// TestIntegrationSFTP round-trips a binary payload through SFTP against a REAL
// sshd, proving file_read/file_write on a remote session are binary-safe (no
// cat/base64 corruption) and that truncation is reported. Opt-in via TERMADA_IT_SSH.
func TestIntegrationSFTP(t *testing.T) {
	addr := os.Getenv("TERMADA_IT_SSH")
	if addr == "" {
		t.Skip("set TERMADA_IT_SSH=host:port (+ _USER/_PASS) to run against a real sshd")
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	dir := t.TempDir()
	v := vault.New(filepath.Join(dir, "v.age"))
	_ = v.Init("vaultpass")
	_ = v.Set("cred", os.Getenv("TERMADA_IT_SSH_PASS"))
	r := NewRunner(v, filepath.Join(dir, "known_hosts"), 8*time.Second)
	srv := fleet.Server{Name: "it", Host: host, Port: port, User: os.Getenv("TERMADA_IT_SSH_USER"), Auth: "cred"}

	content := string([]byte{0, 1, 2, 250, 251, 255, 'h', 'i', '\n', 0}) // binary, incl. NULs
	path := "/tmp/termada-sftp-it.bin"
	n, err := r.SFTPWrite(srv, path, content, "")
	if err != nil || n != len(content) {
		t.Fatalf("sftp write: n=%d err=%v", n, err)
	}
	got, size, trunc, err := r.SFTPRead(srv, path, 0)
	if err != nil {
		t.Fatalf("sftp read: %v", err)
	}
	if string(got) != content || trunc || size != int64(len(content)) {
		t.Fatalf("roundtrip mismatch: got %d bytes (size=%d trunc=%v), want %d", len(got), size, trunc, len(content))
	}
	if g2, _, t2, _ := r.SFTPRead(srv, path, 3); !t2 || len(g2) != 3 {
		t.Fatalf("truncation: got %d bytes trunc=%v, want 3/true", len(g2), t2)
	}
}

// TestIntegrationPortForward opens a local→remote tunnel to the server's own sshd
// and reads the SSH banner THROUGH the tunnel, proving bytes are piped from the
// remote service. Opt-in via TERMADA_IT_SSH.
func TestIntegrationPortForward(t *testing.T) {
	addr := os.Getenv("TERMADA_IT_SSH")
	if addr == "" {
		t.Skip("set TERMADA_IT_SSH=host:port (+ _USER/_PASS) to run against a real sshd")
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	dir := t.TempDir()
	v := vault.New(filepath.Join(dir, "v.age"))
	_ = v.Init("vaultpass")
	_ = v.Set("cred", os.Getenv("TERMADA_IT_SSH_PASS"))
	r := NewRunner(v, filepath.Join(dir, "known_hosts"), 8*time.Second)
	srv := fleet.Server{Name: "it", Host: host, Port: port, User: os.Getenv("TERMADA_IT_SSH_USER"), Auth: "cred"}

	f, err := r.OpenForward(srv, "127.0.0.1:0", "127.0.0.1", 22) // tunnel → the server's own sshd
	if err != nil {
		t.Fatalf("open forward: %v", err)
	}
	defer f.Close()

	conn, err := net.DialTimeout("tcp", f.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial tunnel %s: %v", f.Addr(), err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "SSH-") {
		t.Fatalf("no SSH banner through the tunnel; got %q", string(buf[:n]))
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
	res, err := m.Poll("agent", j2.ID, "")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.Contains(res.StdoutChunk, "tmp") {
		t.Fatalf("remote pwd = %q, want it to contain tmp (cwd persisted over SSH)", res.StdoutChunk)
	}
}
