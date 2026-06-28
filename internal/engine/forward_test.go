package engine

import "testing"

type fakeForwards struct{ started, closed int }

func (f *fakeForwards) Start(server, remoteHost string, remotePort int, localBind string) (ForwardInfo, error) {
	f.started++
	return ForwardInfo{ID: "fwd_1", Server: server, RemoteHost: remoteHost, RemotePort: remotePort, LocalAddr: "127.0.0.1:5599"}, nil
}
func (f *fakeForwards) List() []ForwardInfo  { return []ForwardInfo{{ID: "fwd_1"}} }
func (f *fakeForwards) Close(id string) error { f.closed++; return nil }

func TestPortForwardDelegation(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)

	// No backend wired (in-process): unsupported.
	if _, err := m.PortForward("prod", "127.0.0.1", 5432, ""); err == nil {
		t.Fatal("port_forward without a backend should be unsupported")
	}

	ff := &fakeForwards{}
	m.SetForwardOps(ff)

	info, err := m.PortForward("prod", "127.0.0.1", 5432, "")
	if err != nil || info.LocalAddr != "127.0.0.1:5599" || ff.started != 1 {
		t.Fatalf("start: info=%+v started=%d err=%v", info, ff.started, err)
	}
	if len(m.PortForwardList()) != 1 {
		t.Fatalf("list = %d, want 1", len(m.PortForwardList()))
	}
	if err := m.PortForwardClose("fwd_1"); err != nil || ff.closed != 1 {
		t.Fatalf("close: closed=%d err=%v", ff.closed, err)
	}

	// Argument validation.
	if _, err := m.PortForward("", "h", 1, ""); err == nil {
		t.Fatal("empty server should be rejected")
	}
	if _, err := m.PortForward("prod", "h", 0, ""); err == nil {
		t.Fatal("non-positive remote_port should be rejected")
	}
}
