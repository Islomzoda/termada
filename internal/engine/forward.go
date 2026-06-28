package engine

import "github.com/termada/termada/internal/errs"

// ForwardInfo describes a live local→remote SSH port forward.
type ForwardInfo struct {
	ID         string `json:"id"`
	Server     string `json:"server"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	LocalAddr  string `json:"local_addr"`
}

// ForwardOps manages local→remote SSH port forwards (like `ssh -L`). Wired by the
// daemon (which holds the server inventory + SSH runner); nil in the in-process
// fallback, where port forwarding is unavailable.
type ForwardOps interface {
	Start(server, remoteHost string, remotePort int, localBind string) (ForwardInfo, error)
	List() []ForwardInfo
	Close(id string) error
}

// SetForwardOps installs the port-forward backend (enables port_forward*).
func (m *Manager) SetForwardOps(ops ForwardOps) { m.forwards = ops }

// PortForward opens a local→remote tunnel via server (remoteHost:remotePort
// reached from the server). localBind defaults to an auto-assigned loopback port.
func (m *Manager) PortForward(server, remoteHost string, remotePort int, localBind string) (*ForwardInfo, error) {
	if m.forwards == nil {
		return nil, errs.New(errs.NotSupported, "port forwarding requires the termada daemon (run: termada serve)")
	}
	if server == "" || remoteHost == "" || remotePort <= 0 {
		return nil, errs.New(errs.InvalidArgument, "port_forward needs server, remote_host and a positive remote_port")
	}
	info, err := m.forwards.Start(server, remoteHost, remotePort, localBind)
	if err != nil {
		return nil, errs.New(errs.ServerUnreachable, "open forward via %s: %v", server, err)
	}
	return &info, nil
}

// PortForwardList returns the live forwards.
func (m *Manager) PortForwardList() []ForwardInfo {
	if m.forwards == nil {
		return nil
	}
	return m.forwards.List()
}

// PortForwardClose tears down a forward by id.
func (m *Manager) PortForwardClose(id string) error {
	if m.forwards == nil {
		return errs.New(errs.NotSupported, "port forwarding requires the termada daemon")
	}
	if err := m.forwards.Close(id); err != nil {
		return errs.New(errs.NotFound, "%v", err)
	}
	return nil
}
