// Package vault is the local encrypted credential store (spec §17). The default
// backend is an age-encrypted file (CGO-free); secrets are decrypted into the
// daemon's memory on unlock and are NEVER returned to agents (spec CR-1/§3a).
//
// Threat-model boundary (§3a/CR-5): while unlocked, the passphrase and secrets
// live in process memory and are not protected against a local root with ptrace.
package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/age"
)

// unlockAttemptLimit failed passphrase attempts trip a lockout window
// (unlockLockout) to throttle brute-force against a reachable control socket.
const (
	unlockAttemptLimit = 5
	unlockLockout      = 30 * time.Second
)

// Vault is an age-encrypted secret store.
type Vault struct {
	path string

	mu       sync.RWMutex
	pass     string // held after unlock so writes can re-encrypt
	secrets  map[string]string
	unlocked bool

	failed      int       // consecutive failed unlock attempts (throttle)
	lockedUntil time.Time // unlock refused until this time after too many failures
	lastAccess  atomic.Int64
}

// New returns a vault backed by the file at path (not yet unlocked).
func New(path string) *Vault {
	return &Vault{path: path, secrets: map[string]string{}}
}

// PathString returns the vault file path.
func (v *Vault) PathString() string { return v.path }

// Exists reports whether the vault file is present.
func (v *Vault) Exists() bool {
	_, err := os.Stat(v.path)
	return err == nil
}

// Locked reports whether the vault is currently locked.
func (v *Vault) Locked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return !v.unlocked
}

// Init creates a new empty vault encrypted with pass. Fails if one exists.
func (v *Vault) Init(pass string) error {
	if v.Exists() {
		return fmt.Errorf("vault already exists at %s", v.path)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.secrets = map[string]string{}
	v.pass = pass
	v.unlocked = true
	v.touch()
	return v.saveLocked()
}

// Unlock decrypts the vault file into memory. Repeated wrong passphrases trip a
// lockout window so a reachable control socket can't be brute-forced.
func (v *Vault) Unlock(pass string) error {
	v.mu.Lock()
	if !v.lockedUntil.IsZero() && time.Now().Before(v.lockedUntil) {
		wait := time.Until(v.lockedUntil).Round(time.Second)
		v.mu.Unlock()
		return fmt.Errorf("too many failed unlock attempts; try again in %s", wait)
	}
	v.mu.Unlock()

	data, err := os.ReadFile(v.path)
	if err != nil {
		return fmt.Errorf("read vault: %w", err)
	}
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		return err
	}
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		// age's raw error is a noisy "identity did not match…" chain; for a vault
		// a scrypt-decrypt failure is the wrong passphrase (or a corrupt file).
		v.mu.Lock()
		v.failed++
		if v.failed >= unlockAttemptLimit {
			v.lockedUntil = time.Now().Add(unlockLockout)
			v.failed = 0
		}
		v.mu.Unlock()
		return fmt.Errorf("incorrect vault passphrase (or the vault file is corrupt)")
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	secrets := map[string]string{}
	if len(plain) > 0 {
		if err := json.Unmarshal(plain, &secrets); err != nil {
			return fmt.Errorf("parse vault: %w", err)
		}
	}
	v.mu.Lock()
	v.secrets = secrets
	v.pass = pass
	v.unlocked = true
	v.failed = 0
	v.lockedUntil = time.Time{}
	v.mu.Unlock()
	v.lastAccess.Store(time.Now().UnixNano())
	return nil
}

// touch records that the vault was used, for idle auto-lock.
func (v *Vault) touch() { v.lastAccess.Store(time.Now().UnixNano()) }

// RelockIfIdle locks the vault if it has been unlocked but unused for at least
// idle. Returns true if it locked. A no-op when idle <= 0 or already locked, so
// the daemon can call it on a ticker with the configured idle_relock_ms.
func (v *Vault) RelockIfIdle(idle time.Duration) bool {
	if idle <= 0 || v.Locked() {
		return false
	}
	last := v.lastAccess.Load()
	if last != 0 && time.Since(time.Unix(0, last)) < idle {
		return false
	}
	v.Lock()
	return true
}

// Lock clears decrypted material from memory.
func (v *Vault) Lock() {
	v.mu.Lock()
	for k := range v.secrets {
		v.secrets[k] = ""
		delete(v.secrets, k)
	}
	v.pass = ""
	v.unlocked = false
	v.mu.Unlock()
}

// Set stores or replaces a secret and persists the vault. Requires unlock.
func (v *Vault) Set(name, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.unlocked {
		return fmt.Errorf("vault is locked")
	}
	v.touch()
	v.secrets[name] = value
	return v.saveLocked()
}

// Delete removes a secret and persists. Requires unlock.
func (v *Vault) Delete(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.unlocked {
		return fmt.Errorf("vault is locked")
	}
	v.touch()
	delete(v.secrets, name)
	return v.saveLocked()
}

// Get returns a secret value. This is for internal engine use only (e.g. SSH
// auth, sudo password) and must never be surfaced to an agent.
func (v *Vault) Get(name string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	s, ok := v.secrets[name]
	if ok {
		v.touch()
	}
	return s, ok
}

// Values returns all secret values (for registering with the redactor so they
// can never echo back through output). Internal use only.
func (v *Vault) Values() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.secrets) > 0 {
		v.touch()
	}
	out := make([]string, 0, len(v.secrets))
	for _, s := range v.secrets {
		out = append(out, s)
	}
	return out
}

// List returns secret names only (no values), safe to display.
func (v *Vault) List() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.secrets))
	for k := range v.secrets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// saveLocked encrypts and atomically writes the vault. Caller holds v.mu.
func (v *Vault) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	plain, err := json.Marshal(v.secrets)
	if err != nil {
		return err
	}
	rcpt, err := age.NewScryptRecipient(v.pass)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rcpt)
	if err != nil {
		return err
	}
	if _, err := w.Write(plain); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	tmp := v.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, v.path)
}
