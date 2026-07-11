package sshx

import "testing"

func TestValidateLocalBindLoopbackOnly(t *testing.T) {
	for _, good := range []string{"127.0.0.1:0", "[::1]:8080", "localhost:1234", "[::1%lo0]:0"} {
		if err := validateLocalBind(good); err != nil {
			t.Errorf("validateLocalBind(%q): %v", good, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:0", "[::]:8080", ":0", "192.0.2.1:9000", "example.com:80", "127.0.0.1"} {
		if err := validateLocalBind(bad); err == nil {
			t.Errorf("validateLocalBind(%q) accepted a public/invalid bind", bad)
		}
	}
}

func TestForwardConnectionLimit(t *testing.T) {
	f := &Forward{slots: make(chan struct{}, maxForwardConnections)}
	for i := 0; i < maxForwardConnections; i++ {
		if !f.reserve() {
			t.Fatalf("connection %d rejected below limit", i+1)
		}
	}
	if f.reserve() {
		t.Fatal("connection above limit was accepted")
	}
	f.release()
	if !f.reserve() {
		t.Fatal("slot was not reusable after release")
	}
}
