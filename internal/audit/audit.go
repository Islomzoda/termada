// Package audit is the append-only, tamper-evident audit log (spec §8.5/SEC-3).
// Each record is hash-chained to its predecessor, so any after-the-fact edit or
// deletion breaks the chain and is detectable on Verify.
//
// Honest boundary (§3a): this is tamper-EVIDENT, not tamper-proof. It detects
// tampering by an agent or any process without the chain history; it does not
// stop a local root who rewrites the whole chain. Real tamper-resistance needs
// the head hash anchored outside the file (keychain/TPM/remote) — a later phase.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/output"
)

// Record is one audit entry.
type Record struct {
	Seq       int64          `json:"seq"`
	Time      time.Time      `json:"time"`
	Type      string         `json:"type"`
	AgentID   string         `json:"agent_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	JobID     string         `json:"job_id,omitempty"`
	Message   string         `json:"message,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	PrevHash  string         `json:"prev_hash"`
	Hash      string         `json:"hash"`
}

// Logger appends hash-chained records to a file.
type Logger struct {
	path     string
	redactor *output.Redactor

	mu       sync.Mutex
	f        *os.File
	w        *bufio.Writer
	lastHash string
	seq      int64
	size     int64
	maxBytes int64
	healthy  bool
}

// SetMaxBytes sets the segment size at which the log rotates (0 = default 10MB).
func (l *Logger) SetMaxBytes(n int64) {
	l.mu.Lock()
	l.maxBytes = n
	l.mu.Unlock()
}

// Healthy reports whether the last write succeeded. When false the audit log
// cannot be trusted to record actions, so callers should fail closed (RE-7).
func (l *Logger) Healthy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.healthy
}

func (l *Logger) maxSegment() int64 {
	if l.maxBytes > 0 {
		return l.maxBytes
	}
	return 10 << 20 // 10 MiB
}

// Open opens (or creates) the audit log at path, recovering the last hash and
// sequence number so the chain continues correctly across restarts.
func Open(path string, redactor *output.Redactor) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &Logger{path: path, redactor: redactor, healthy: true}
	if err := l.recover(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.f = f
	l.w = bufio.NewWriter(f)
	if fi, err := f.Stat(); err == nil {
		l.size = fi.Size()
	}
	return l, nil
}

// rotate seals the current segment (renaming it with a timestamp) and starts a
// fresh one. The hash chain continues: the next record's prev_hash is the sealed
// segment's head, so cross-segment tampering is still detectable. Caller holds
// l.mu.
func (l *Logger) rotate(stamp string) error {
	if err := l.w.Flush(); err != nil {
		return err
	}
	if err := l.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(l.path, l.path+"."+stamp); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	l.w = bufio.NewWriter(f)
	l.size = 0
	return nil
}

// recover scans the existing log to find the last hash and sequence number.
func (l *Logger) recover() error {
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("corrupt audit record: %w", err)
		}
		l.lastHash = rec.Hash
		l.seq = rec.Seq
	}
	return sc.Err()
}

// hashOf computes the chain hash of a record (over everything except Hash).
func hashOf(rec Record) string {
	rec.Hash = ""
	b, _ := json.Marshal(rec)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Append writes a record, chaining it to the previous one and redacting its
// message/data. It fsyncs before returning (durable, single-writer).
func (l *Logger) Append(rec Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	rec.Seq = l.seq
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	if l.redactor != nil {
		rec.Message = l.redactor.Redact(rec.Message)
		rec.Data = redactData(l.redactor, rec.Data)
	}
	rec.PrevHash = l.lastHash
	rec.Hash = hashOf(rec)

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if l.size > 0 && l.size+int64(len(line)+1) > l.maxSegment() {
		stamp := fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), l.seq)
		if err := l.rotate(stamp); err != nil {
			l.healthy = false
			return err
		}
	}
	if _, err := l.w.Write(append(line, '\n')); err != nil {
		l.healthy = false
		return err
	}
	if err := l.w.Flush(); err != nil {
		l.healthy = false
		return err
	}
	if err := l.f.Sync(); err != nil {
		l.healthy = false
		return err
	}
	l.size += int64(len(line) + 1)
	l.lastHash = rec.Hash
	l.healthy = true
	return nil
}

// SetRedactor sets the redactor used to mask secrets before writing.
func (l *Logger) SetRedactor(r *output.Redactor) {
	l.mu.Lock()
	l.redactor = r
	l.mu.Unlock()
}

// FromBus converts a bus event into an audit record and appends it.
func (l *Logger) FromBus(e bus.Event) {
	_ = l.Append(Record{
		Time:      e.Time,
		Type:      e.Type,
		AgentID:   e.AgentID,
		SessionID: e.SessionID,
		JobID:     e.JobID,
		Message:   e.Message,
		Data:      e.Data,
	})
}

// Close flushes and closes the log.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		l.w.Flush()
	}
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

func redactData(r *output.Redactor, data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		if s, ok := v.(string); ok {
			out[k] = r.Redact(s)
		} else {
			out[k] = v
		}
	}
	return out
}

// Verify checks the hash chain of the log at path. It returns the number of
// records verified and an error describing the first break, if any.
func Verify(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var n, expectSeq int64
	prev := ""
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return n, fmt.Errorf("record %d: corrupt JSON: %w", n+1, err)
		}
		// Segments (from rotation) start at an arbitrary seq and carry the prior
		// segment's head as prev_hash, so the base is taken from the first record;
		// every record's own hash is still verified (which covers prev_hash too).
		if first {
			expectSeq = rec.Seq
			first = false
		} else {
			if rec.Seq != expectSeq {
				return n, fmt.Errorf("record %d: seq=%d, expected %d", n+1, rec.Seq, expectSeq)
			}
			if rec.PrevHash != prev {
				return n, fmt.Errorf("record %d (seq %d): prev_hash mismatch — chain broken", n+1, rec.Seq)
			}
		}
		if got := hashOf(rec); got != rec.Hash {
			return n, fmt.Errorf("record %d (seq %d): hash mismatch — record altered", n+1, rec.Seq)
		}
		prev = rec.Hash
		expectSeq++
		n++
	}
	return n, sc.Err()
}

// Tail returns up to the last n records.
func (l *Logger) Tail(n int) ([]Record, error) {
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var all []Record
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return all, err
		}
		all = append(all, rec)
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, sc.Err()
}
