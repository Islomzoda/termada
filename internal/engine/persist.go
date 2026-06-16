package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// maxPersistedJobs caps how many job records are kept in the on-disk registry.
const maxPersistedJobs = 500

// EnablePersistence points the manager at an on-disk job registry and recovers
// the previous run's jobs (spec RE-1/RE-2). Recovery is honest about the local
// PTY reality (fork R1): any job that was still running cannot have survived a
// crash — its process and output stream are gone — so it is recovered as
// `orphaned`, not `running`. Already-terminal jobs are kept as history.
func (m *Manager) EnablePersistence(path string) error {
	m.mu.Lock()
	m.persistPath = path
	m.mu.Unlock()

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

// persist atomically writes the current job registry (live + recovered) to disk.
// It is a no-op until EnablePersistence is called.
func (m *Manager) persist() {
	m.mu.Lock()
	path := m.persistPath
	if path == "" {
		m.mu.Unlock()
		return
	}
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
		return
	}
	writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
