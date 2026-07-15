package engine

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/termada/termada/internal/bus"
)

// maxPersistedJobs caps how many job records are kept in the on-disk registry.
const maxPersistedJobs = 500

// EnablePersistence points the manager at an on-disk job registry and recovers
// the previous run's jobs (spec RE-1/RE-2). Recovery is honest about the local
// PTY reality (fork R1): any job that was still running cannot have survived a
// crash — its process and output stream are gone — so it is recovered as
// `orphaned`, not `running`. Already-terminal jobs are kept as history.
func (m *Manager) EnablePersistence(path string) (retErr error) {
	m.persistMu.Lock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		m.persistMu.Unlock()
		return managerClosedError()
	}
	m.persistPath = path
	defer func() {
		m.persistErr = retErr
		m.persistMu.Unlock()
		if retErr != nil {
			m.reportPersistenceError("recover", path, retErr)
		}
	}()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snap []Info
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	recovered := make([]Info, 0, len(snap))
	for _, in := range snap {
		// Persistence stores metadata only. PTY buffers and stream handles never
		// survive a daemon restart, even for jobs that were already terminal.
		in.StreamAvailable = false
		if !in.Status.Terminal() {
			in.Status = StatusOrphaned
			if in.Reason == "" {
				in.Reason = "daemon restarted; local process and output were lost"
			}
		}
		recovered = append(recovered, in)
	}
	m.mu.Lock()
	m.recovered = recovered
	m.mu.Unlock()
	return nil
}

// PersistenceStatus is the current health of the on-disk job registry. A
// disabled registry is healthy; once enabled, the last recovery/write error is
// retained until a later successful operation clears it.
type PersistenceStatus struct {
	Enabled bool   `json:"enabled"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

// PersistenceStatus returns a race-free snapshot suitable for health/status
// endpoints. It makes asynchronous write failures observable to callers even
// when the operation that triggered the write cannot safely return an error.
func (m *Manager) PersistenceStatus() PersistenceStatus {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	status := PersistenceStatus{Enabled: m.persistPath != "", Healthy: m.persistErr == nil}
	if m.persistErr != nil {
		status.Error = m.persistErr.Error()
	}
	return status
}

// persist atomically writes the current job registry (live + recovered) to disk.
// It is a no-op until EnablePersistence is called.
func (m *Manager) persist() (retErr error) {
	m.persistMu.Lock()
	path := m.persistPath
	defer func() {
		m.persistErr = retErr
		m.persistMu.Unlock()
		if retErr != nil {
			m.reportPersistenceError("write", path, retErr)
		}
	}()

	if path == "" {
		return nil
	}
	m.mu.Lock()
	infos := make([]Info, 0, len(m.jobs)+len(m.recovered))
	for _, j := range m.jobs {
		infos = append(infos, j.info())
	}
	infos = append(infos, m.recovered...)
	m.mu.Unlock()

	if len(infos) > maxPersistedJobs {
		infos = infos[len(infos)-maxPersistedJobs:]
	}
	data, err := json.MarshalIndent(infos, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

func (m *Manager) reportPersistenceError(operation, path string, err error) {
	_ = m.publish(bus.Event{
		Type:    bus.EvPersistenceError,
		Message: err.Error(),
		Data:    map[string]any{"operation": operation, "path": path},
	})
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".termada-registry-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
