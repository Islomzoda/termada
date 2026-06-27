package vault

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Repeated wrong passphrases trip a lockout so a reachable socket can't be
// brute-forced; even the correct passphrase is refused during the window.
func TestUnlockThrottle(t *testing.T) {
	v := New(filepath.Join(t.TempDir(), "v.age"))
	if err := v.Init("correct"); err != nil {
		t.Fatalf("init: %v", err)
	}
	v.Lock()
	for i := 0; i < unlockAttemptLimit; i++ {
		if err := v.Unlock("wrong"); err == nil {
			t.Fatal("wrong passphrase accepted")
		}
	}
	err := v.Unlock("correct")
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected throttle after %d failures, got %v", unlockAttemptLimit, err)
	}
}

// An unlocked-but-idle vault auto-locks; a fresh or 0 idle is a no-op.
func TestRelockIfIdle(t *testing.T) {
	v := New(filepath.Join(t.TempDir(), "v.age"))
	if err := v.Init("p"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if v.RelockIfIdle(time.Hour) {
		t.Fatal("relocked while not idle")
	}
	if v.RelockIfIdle(0) {
		t.Fatal("relocked with idle=0 (disabled)")
	}
	time.Sleep(20 * time.Millisecond)
	if !v.RelockIfIdle(10 * time.Millisecond) {
		t.Fatal("should have relocked when idle past threshold")
	}
	if !v.Locked() {
		t.Fatal("vault should be locked after idle relock")
	}
}

func TestVaultRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.age")
	v := New(path)
	if v.Exists() {
		t.Fatal("vault should not exist yet")
	}
	if err := v.Init("hunter2"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := v.Set("prod-ssh-key", "super-secret-value"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if names := v.List(); len(names) != 1 || names[0] != "prod-ssh-key" {
		t.Fatalf("list = %v", names)
	}

	// Reopen with a fresh instance to prove it persisted and decrypts.
	v2 := New(path)
	if !v2.Exists() {
		t.Fatal("vault file missing after init")
	}
	if !v2.Locked() {
		t.Fatal("fresh instance should be locked")
	}
	if err := v2.Unlock("hunter2"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	got, ok := v2.Get("prod-ssh-key")
	if !ok || got != "super-secret-value" {
		t.Fatalf("get = %q, %v", got, ok)
	}
}

func TestVaultWrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.age")
	v := New(path)
	if err := v.Init("correct"); err != nil {
		t.Fatal(err)
	}
	_ = v.Set("k", "v")

	v2 := New(path)
	if err := v2.Unlock("wrong"); err == nil {
		t.Fatal("unlock with wrong passphrase should fail")
	}
}

func TestVaultLockClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.age")
	v := New(path)
	_ = v.Init("p")
	_ = v.Set("k", "v")
	v.Lock()
	if !v.Locked() {
		t.Fatal("should be locked")
	}
	if _, ok := v.Get("k"); ok {
		t.Fatal("secret should be cleared after lock")
	}
}
