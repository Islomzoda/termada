package sshx

import (
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/termada/termada/internal/fleet"
	"golang.org/x/crypto/ssh"
)

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
	client, err := r.dial(server)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", localBind)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	f := &Forward{ln: ln, client: client, remote: fmt.Sprintf("%s:%d", remoteHost, remotePort), conns: map[net.Conn]struct{}{}}
	go f.serve()
	return f, nil
}

func (f *Forward) serve() {
	for {
		local, err := f.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go f.handle(local)
	}
}

func (f *Forward) handle(local net.Conn) {
	remote, err := f.client.Dial("tcp", f.remote)
	if err != nil {
		_ = local.Close()
		return
	}
	f.track(local, true)
	f.track(remote, true)
	defer func() {
		f.track(local, false)
		f.track(remote, false)
		_ = local.Close()
		_ = remote.Close()
	}()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done // first side to finish tears the pair down (deferred closes unblock the other)
}

func (f *Forward) track(c net.Conn, add bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if add {
		if f.closed {
			_ = c.Close()
			return
		}
		f.conns[c] = struct{}{}
	} else {
		delete(f.conns, c)
	}
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
