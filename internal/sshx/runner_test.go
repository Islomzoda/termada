package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
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
