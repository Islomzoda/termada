package engine

import (
	"net"
	"strconv"
	"strings"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/policy"
)

// ForwardInfo describes a live local→remote SSH port forward.
type ForwardInfo struct {
	ID         string `json:"id"`
	Server     string `json:"server"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	LocalAddr  string `json:"local_addr"`
	Owner      string `json:"owner,omitempty"`
}

// ForwardOps manages local→remote SSH port forwards (like `ssh -L`). Wired by the
// daemon (which holds the server inventory + SSH runner); nil in the in-process
// fallback, where port forwarding is unavailable.
type ForwardOps interface {
	Start(owner, server, remoteHost string, remotePort int, localBind string) (ForwardInfo, error)
	List(owner string) []ForwardInfo
	Close(owner, id string) error
}

// SetForwardOps installs the port-forward backend (enables port_forward*).
func (m *Manager) SetForwardOps(ops ForwardOps) {
	m.mu.Lock()
	if !m.closed {
		m.forwards = ops
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	shutdownForwardOps(ops)
}

// PortForward opens a local→remote tunnel via server (remoteHost:remotePort
// reached from the server). localBind defaults to an auto-assigned loopback port.
func (m *Manager) PortForward(owner, server, remoteHost string, remotePort int, localBind string) (*ForwardInfo, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, managerClosedError()
	}
	forwards := m.forwards
	m.mu.Unlock()
	if forwards == nil {
		return nil, errs.New(errs.NotSupported, "port forwarding requires the termada daemon (run: termada serve)")
	}
	if strings.TrimSpace(server) == "" || strings.TrimSpace(remoteHost) == "" || remotePort <= 0 || remotePort > 65535 {
		return nil, errs.New(errs.InvalidArgument, "port_forward needs server, remote_host and a remote_port between 1 and 65535")
	}
	if len(server) > 255 || len(remoteHost) > 1024 || len(localBind) > 128 ||
		strings.IndexFunc(server+remoteHost+localBind, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return nil, errs.New(errs.InvalidArgument, "port-forward address fields are too long or contain control characters")
	}
	if !forwardBindLoopback(localBind) {
		return nil, errs.New(errs.DeniedByPolicy, "non-loopback local_bind is refused; port forwards must listen on loopback")
	}
	cmd := []string{"port_forward", server, remoteHost, strconv.Itoa(remotePort)}
	if localBind != "" {
		cmd = append(cmd, localBind)
	}
	if owner != "" && m.pol != nil {
		dec := m.pol.Evaluate(m.agentPolicies[owner], cmd)
		switch dec.Decision {
		case policy.Deny:
			m.publish(bus.Event{Type: bus.EvPolicyDenied, AgentID: owner, Message: strings.Join(cmd, " "),
				Data: map[string]any{"reason": dec.Reason, "matched": dec.Matched, "transport": "port_forward"}})
			return nil, errs.New(errs.DeniedByPolicy, "port forward denied by policy (%s)", dec.Reason)
		case policy.Confirm:
			return nil, errs.New(errs.DeniedByPolicy, "port forward needs human approval (matched %q); open it from the authenticated dashboard", dec.Matched)
		}
	}
	if m.auditOK != nil && !m.auditOK() {
		return nil, errs.New(errs.Internal, "audit log unavailable - refusing port forward (fail-closed)")
	}
	info, err := forwards.Start(owner, server, remoteHost, remotePort, localBind)
	if err != nil {
		return nil, errs.New(errs.ServerUnreachable, "open forward via %s: %v", server, err)
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		_ = forwards.Close(owner, info.ID)
		return nil, managerClosedError()
	}
	info.Owner = owner
	if err := m.publish(bus.Event{Type: "forward.started", AgentID: owner, Message: strings.Join(cmd, " "),
		Data: map[string]any{"forward_id": info.ID, "server": server, "remote_host": remoteHost, "remote_port": remotePort, "local_addr": info.LocalAddr}}); err != nil {
		_ = forwards.Close(owner, info.ID)
		return nil, errs.New(errs.Internal, "audit log unavailable - closed unrecorded port forward: %v", err)
	}
	return &info, nil
}

// PortForwardList returns live forwards scoped to owner; owner="" is operator.
func (m *Manager) PortForwardList(owner string) []ForwardInfo {
	m.mu.Lock()
	forwards := m.forwards
	m.mu.Unlock()
	if forwards == nil {
		return nil
	}
	return forwards.List(owner)
}

// PortForwardClose tears down a forward by id.
func (m *Manager) PortForwardClose(owner, id string) error {
	m.mu.Lock()
	forwards := m.forwards
	m.mu.Unlock()
	if forwards == nil {
		return errs.New(errs.NotSupported, "port forwarding requires the termada daemon")
	}
	if err := forwards.Close(owner, id); err != nil {
		return errs.New(errs.NotFound, "%v", err)
	}
	m.publish(bus.Event{Type: "forward.closed", AgentID: owner, Message: id,
		Data: map[string]any{"forward_id": id}})
	return nil
}

func shutdownForwardOps(forwards ForwardOps) {
	if forwards == nil {
		return
	}
	if shutdown, ok := forwards.(interface{ Shutdown() }); ok {
		shutdown.Shutdown()
		return
	}
	for _, forward := range forwards.List("") {
		_ = forwards.Close("", forward.ID)
	}
}

func forwardBindLoopback(bind string) bool {
	if bind == "" {
		return true
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if i := strings.LastIndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
