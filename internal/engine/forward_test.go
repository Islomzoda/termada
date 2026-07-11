package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/policy"
)

type fakeForwards struct {
	started, closed int
	owner           string
}

func (f *fakeForwards) Start(owner, server, remoteHost string, remotePort int, localBind string) (ForwardInfo, error) {
	f.started++
	f.owner = owner
	return ForwardInfo{ID: "fwd_1", Owner: owner, Server: server, RemoteHost: remoteHost, RemotePort: remotePort, LocalAddr: "127.0.0.1:5599"}, nil
}
func (f *fakeForwards) List(owner string) []ForwardInfo {
	if owner != "" && owner != f.owner {
		return nil
	}
	return []ForwardInfo{{ID: "fwd_1", Owner: f.owner}}
}
func (f *fakeForwards) Close(owner, id string) error {
	if owner != "" && owner != f.owner {
		return fmt.Errorf("forward %q not found", id)
	}
	f.closed++
	return nil
}

func TestPortForwardDelegation(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)

	// No backend wired (in-process): unsupported.
	if _, err := m.PortForward("agent", "prod", "127.0.0.1", 5432, ""); err == nil {
		t.Fatal("port_forward without a backend should be unsupported")
	}

	ff := &fakeForwards{}
	m.SetForwardOps(ff)

	info, err := m.PortForward("agent", "prod", "127.0.0.1", 5432, "")
	if err != nil || info.LocalAddr != "127.0.0.1:5599" || ff.started != 1 {
		t.Fatalf("start: info=%+v started=%d err=%v", info, ff.started, err)
	}
	if len(m.PortForwardList("agent")) != 1 || len(m.PortForwardList("other")) != 0 {
		t.Fatalf("owner-scoped list failed")
	}
	if err := m.PortForwardClose("other", "fwd_1"); err == nil {
		t.Fatal("another owner closed the forward")
	}
	if err := m.PortForwardClose("agent", "fwd_1"); err != nil || ff.closed != 1 {
		t.Fatalf("close: closed=%d err=%v", ff.closed, err)
	}

	// Argument validation.
	if _, err := m.PortForward("agent", "", "h", 1, ""); err == nil {
		t.Fatal("empty server should be rejected")
	}
	if _, err := m.PortForward("agent", "prod", "h", 0, ""); err == nil {
		t.Fatal("non-positive remote_port should be rejected")
	}
	if _, err := m.PortForward("agent", "prod", "h", 80, "0.0.0.0:0"); err == nil {
		t.Fatal("agent non-loopback bind should require an operator")
	}
	if _, err := m.PortForward("", "prod", "h", 80, "0.0.0.0:0"); err == nil {
		t.Fatal("operator non-loopback bind should also fail closed")
	}
	if _, err := m.PortForward("agent", "prod", "h", 70000, ""); err == nil {
		t.Fatal("out-of-range remote_port should be rejected before the backend")
	}
}

func TestPortForwardPolicyAndAuditHealth(t *testing.T) {
	ff := &fakeForwards{}
	m := NewManager(DefaultConfig())
	m.SetForwardOps(ff)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"deny":    {Deny: []string{"port_forward*"}},
		"confirm": {Confirm: []string{"port_forward*"}},
	}), map[string]string{"denied": "deny", "confirm": "confirm"})
	if _, err := m.PortForward("denied", "prod", "127.0.0.1", 5432, ""); err == nil {
		t.Fatal("policy-denied forward was opened")
	}
	if _, err := m.PortForward("confirm", "prod", "127.0.0.1", 5432, ""); err == nil {
		t.Fatal("confirmation-gated forward ran without operator approval")
	}
	if ff.started != 0 {
		t.Fatalf("backend started %d denied forwards", ff.started)
	}
	m.SetAuditHealth(func() bool { return false })
	if _, err := m.PortForward("allowed", "prod", "127.0.0.1", 5432, ""); err == nil {
		t.Fatal("forward opened while audit was unhealthy")
	}
}

func TestPortForwardClosesWhenAuditAppendFails(t *testing.T) {
	ff := &fakeForwards{}
	m := NewManager(DefaultConfig())
	m.SetForwardOps(ff)
	b := bus.New(10)
	m.SetBus(b)
	cancel := b.SubscribeReliable(func(event bus.Event) error {
		if event.Type == "forward.started" {
			return errors.New("disk full")
		}
		return nil
	})
	t.Cleanup(cancel)

	if _, err := m.PortForward("agent", "prod", "127.0.0.1", 5432, ""); err == nil {
		t.Fatal("forward succeeded when its audit record failed")
	}
	if ff.started != 1 || ff.closed != 1 {
		t.Fatalf("unrecorded forward cleanup: started=%d closed=%d", ff.started, ff.closed)
	}
}

func TestShutdownClosesPortForwards(t *testing.T) {
	ff := &fakeForwards{}
	m := NewManager(DefaultConfig())
	m.SetForwardOps(ff)
	if _, err := m.PortForward("agent", "prod", "127.0.0.1", 5432, ""); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	if ff.closed != 1 {
		t.Fatalf("Shutdown closed %d forwards, want 1", ff.closed)
	}
}
