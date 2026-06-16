package vault

import (
	"path/filepath"
	"testing"
)

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
