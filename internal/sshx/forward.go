package sshx

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/termada/termada/internal/fleet"
	"golang.org/x/crypto/ssh"
)

// maxForwardConnections bounds sockets and copy goroutines owned by one
// forward. Excess connections are rejected immediately and can retry later.
const maxForwardConnections = 64

// Forward is a live local→remote TCP tunnel over SSH (like `ssh -L`): it listens
// on a local address and pipes each accepted connection to remoteHost:remotePort
// reached *from the server*. Close stops the listener and tears down the SSH
// connection (and all in-flight tunneled conns).
type Forward struct {
	ln     net.Listener
	client *ssh.Client
	remote string

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	slots  chan struct{}
}

// Addr is the local address the tunnel listens on (host:port).
func (f *Forward) Addr() string { return f.ln.Addr().String() }

// OpenForward dials server and starts a local listener that tunnels to
// remoteHost:remotePort via the server. localBind defaults to 127.0.0.1:0 (an
// auto-assigned loopback port) so the tunnel isn't exposed on the network.
func (r *Runner) OpenForward(server fleet.Server, localBind, remoteHost string, remotePort int) (*Forward, error) {
	if localBind == "" {
		localBind = "127.0.0.1:0"
	}
	if err := validateLocalBind(localBind); err != nil {
		return nil, err
	}
	if strings.TrimSpace(remoteHost) == "" || remotePort < 1 || remotePort > 65535 {
		return nil, fmt.Errorf("remote address requires a host and port in 1..65535")
	}
	client, err := r.dial(server)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", localBind)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if addr, ok := ln.Addr().(*net.TCPAddr); !ok || addr.IP == nil || !addr.IP.IsLoopback() {
		_ = ln.Close()
		_ = client.Close()
		return nil, fmt.Errorf("resolved local bind %q is not loopback", localBind)
	}
	f := &Forward{
		ln: ln, client: client,
		remote: net.JoinHostPort(remoteHost, strconv.Itoa(remotePort)),
		conns:  map[net.Conn]struct{}{}, slots: make(chan struct{}, maxForwardConnections),
	}
	go f.serve()
	return f, nil
}

func validateLocalBind(localBind string) error {
	host, _, err := net.SplitHostPort(localBind)
	if err != nil {
		return fmt.Errorf("invalid local bind %q: %w", localBind, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if i := strings.LastIndexByte(host, '%'); i >= 0 {
		host = host[:i] // IPv6 zone identifier
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("local bind %q is not loopback; public SSH forwards are disabled", localBind)
	}
	return nil
}

func (f *Forward) serve() {
	for {
		local, err := f.ln.Accept()
		if err != nil {
			return // listener closed
		}
		if f.reserve() {
			go func() {
				defer f.release()
				f.handle(local)
			}()
		} else {
			_ = local.Close()
		}
	}
}

func (f *Forward) reserve() bool {
	select {
	case f.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (f *Forward) release() { <-f.slots }

func (f *Forward) handle(local net.Conn) {
	if !f.track(local, true) {
		return
	}
	defer func() {
		f.track(local, false)
		_ = local.Close()
	}()
	remote, err := f.client.Dial("tcp", f.remote)
	if err != nil {
		return
	}
	if !f.track(remote, true) {
		_ = remote.Close()
		return
	}
	defer func() {
		f.track(remote, false)
		_ = remote.Close()
	}()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done // first side to finish tears the pair down (deferred closes unblock the other)
}

func (f *Forward) track(c net.Conn, add bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if add {
		if f.closed {
			_ = c.Close()
			return false
		}
		f.conns[c] = struct{}{}
	} else {
		delete(f.conns, c)
	}
	return true
}

// Close stops accepting, drops the SSH connection, and closes every tunneled
// conn so io.Copy loops unblock and their goroutines exit.
func (f *Forward) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()

	_ = f.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	return f.client.Close()
}
