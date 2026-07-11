// Package audit is the append-only, tamper-evident audit log (spec §8.5/SEC-3).
// Each record is hash-chained to its predecessor, so any after-the-fact edit or
// deletion breaks the chain and is detectable on VerifyAll.
//
// Honest boundary (§3a): this is tamper-EVIDENT, not tamper-proof. It detects
// tampering by an agent or any process without the chain history; it does not
// stop a local root who rewrites the whole chain and its retention checkpoint.
// Real tamper-resistance needs the head hash anchored outside the file
// (keychain/TPM/remote) — a later phase.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
	// maxSegments bounds rotated segments; zero selects the secure default.
	maxSegments int
	healthy     bool
	lastErr     error
}

var errLoggerClosed = errors.New("audit logger is closed")

const maxAuditRecordBytes = 1 << 20

// ErrRecordTooLarge is a caller validation error, not a storage failure. The
// rejected record is not written, but the logger remains usable for later
// bounded records.
var ErrRecordTooLarge = errors.New("audit record exceeds size limit")

// SetMaxBytes sets the segment size at which the log rotates (0 = default 10MB).
func (l *Logger) SetMaxBytes(n int64) {
	l.mu.Lock()
	l.maxBytes = n
	l.mu.Unlock()
}

// SetMaxSegments sets the maximum number of rotated segments retained beside
// the active file. Values below one restore the default. The new bound is
// enforced on the next rotation; the default is also enforced during Open.
func (l *Logger) SetMaxSegments(n int) {
	l.mu.Lock()
	l.maxSegments = n
	l.mu.Unlock()
}

// Healthy reports whether the logger has durably recorded every accepted append
// and remains open. Caller-validation errors such as an oversized record reject
// only that record. Once false it stays false, so callers can fail closed after
// any storage or serialization gap (RE-7).
func (l *Logger) Healthy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.healthy
}

// LastError reports the error that latched the logger unhealthy. Audit health
// remains false after a failed append because that action can no longer be
// retroactively made durable.
func (l *Logger) LastError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
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
	if err := l.enforceRetentionLocked(); err != nil {
		return nil, fmt.Errorf("enforce audit retention: %w", err)
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
	if err := syncDirectory(filepath.Dir(l.path)); err != nil {
		return err
	}
	return l.enforceRetentionLocked()
}

// recover scans the existing log to find the last hash and sequence number.
func (l *Logger) recover() error {
	seq, lastHash, err := verifyAllState(l.path)
	if err != nil {
		return fmt.Errorf("recover audit chain: %w", err)
	}
	l.seq = seq
	l.lastHash = lastHash
	return nil
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
	if !l.healthy {
		if l.lastErr == nil {
			l.lastErr = errors.New("audit logger is unhealthy")
		}
		return fmt.Errorf("audit logger is unhealthy: %w", l.lastErr)
	}
	if l.f == nil || l.w == nil {
		return l.failLocked(errLoggerClosed)
	}
	rec.Seq = l.seq + 1
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
		return l.failLocked(fmt.Errorf("marshal audit record: %w", err))
	}
	if len(line) > maxAuditRecordBytes {
		return fmt.Errorf("%w: got %d bytes, limit is %d", ErrRecordTooLarge, len(line), maxAuditRecordBytes)
	}
	if l.size > 0 && l.size+int64(len(line)+1) > l.maxSegment() {
		stamp := fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), rec.Seq)
		if err := l.rotate(stamp); err != nil {
			return l.failLocked(fmt.Errorf("rotate audit log: %w", err))
		}
	}
	if _, err := l.w.Write(append(line, '\n')); err != nil {
		return l.failLocked(fmt.Errorf("write audit record: %w", err))
	}
	if err := l.w.Flush(); err != nil {
		return l.failLocked(fmt.Errorf("flush audit record: %w", err))
	}
	if err := l.f.Sync(); err != nil {
		return l.failLocked(fmt.Errorf("sync audit record: %w", err))
	}
	l.size += int64(len(line) + 1)
	l.lastHash = rec.Hash
	l.seq = rec.Seq
	return nil
}

func (l *Logger) failLocked(err error) error {
	l.healthy = false
	l.lastErr = err
	return err
}

// SetRedactor sets the redactor used to mask secrets before writing.
func (l *Logger) SetRedactor(r *output.Redactor) {
	l.mu.Lock()
	l.redactor = r
	l.mu.Unlock()
}

// FromBus converts a bus event into an audit record and appends it. The error is
// intentionally returned so reliable bus delivery can propagate durability
// failures back to the publisher.
func (l *Logger) FromBus(e bus.Event) error {
	return l.Append(Record{
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
	if l.f == nil {
		return nil
	}
	var closeErr error
	if l.w != nil {
		if err := l.w.Flush(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("flush audit log: %w", err))
		}
	}
	if err := l.f.Close(); err != nil {
		closeErr = errors.Join(closeErr, fmt.Errorf("close audit log: %w", err))
	}
	l.f = nil
	l.w = nil
	if closeErr != nil {
		return l.failLocked(closeErr)
	}
	if l.healthy {
		l.healthy = false
		l.lastErr = errLoggerClosed
	}
	return nil
}

func redactData(r *output.Redactor, data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		out[r.Redact(k)] = redactValue(r, v)
	}
	return out
}

func redactValue(r *output.Redactor, value any) any {
	return redactValueDepth(r, value, 0)
}

func redactValueDepth(r *output.Redactor, value any, depth int) any {
	// JSON itself rejects cycles. The depth bound keeps redaction from recursing
	// forever before json.Marshal can surface that error and latch audit unhealthy.
	if depth > 64 {
		return value
	}
	switch v := value.(type) {
	case string:
		return r.Redact(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[r.Redact(key)] = redactValueDepth(r, item, depth+1)
		}
		return out
	}

	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.String:
		return r.Redact(rv.String())
	case reflect.Slice, reflect.Array:
		// Preserve byte slices: encoding/json deliberately represents them as
		// base64, not as a structured list of text values.
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return value
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = redactValueDepth(r, rv.Index(i).Interface(), depth+1)
		}
		return out
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return value
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[r.Redact(iter.Key().String())] = redactValueDepth(r, iter.Value().Interface(), depth+1)
		}
		return out
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return redactValueDepth(r, rv.Elem().Interface(), depth+1)
	default:
		return value
	}
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

type sealedSegment struct {
	path  string
	order int64
}

// sealedSegments returns rotated segments in the sequence order encoded by the
// logger in their filename. Treating malformed or duplicate sequence suffixes
// as corruption prevents a renamed segment from silently changing chain order.
func sealedSegments(basePath string) ([]sealedSegment, error) {
	dir := filepath.Dir(basePath)
	base := filepath.Base(basePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	prefix := base + "."
	var segments []sealedSegment
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if entry.Name() == base+archiveCheckpointSuffix {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect sealed audit segment %q: %w", entry.Name(), err)
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("sealed audit segment %q is not a regular file", entry.Name())
		}
		suffix := strings.TrimPrefix(entry.Name(), prefix)
		dash := strings.LastIndexByte(suffix, '-')
		if dash < 0 || dash == len(suffix)-1 {
			return nil, fmt.Errorf("malformed sealed audit segment name %q", entry.Name())
		}
		order, err := strconv.ParseInt(suffix[dash+1:], 10, 64)
		if err != nil || order <= 0 {
			return nil, fmt.Errorf("malformed sealed audit segment sequence in %q", entry.Name())
		}
		segments = append(segments, sealedSegment{path: filepath.Join(dir, entry.Name()), order: order})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].order < segments[j].order })
	for i := 1; i < len(segments); i++ {
		if segments[i].order == segments[i-1].order {
			return nil, fmt.Errorf("duplicate sealed audit segment sequence %d", segments[i].order)
		}
	}
	return segments, nil
}

// VerifyAll verifies every retained rotated segment followed by the active
// basePath as one continuous chain. Without a retention checkpoint it requires
// sequence 1 and the empty genesis hash. With a checkpoint it verifies the
// checkpoint checksum, resumes at its committed sequence/hash boundary, and
// still detects deletion or reordering inside the retained suffix.
func VerifyAll(basePath string) (int64, error) {
	total, _, err := verifyAllState(basePath)
	return total, err
}

func verifyAllState(basePath string) (int64, string, error) {
	checkpoint, hasCheckpoint, err := loadArchiveCheckpoint(basePath)
	if err != nil {
		return 0, "", err
	}
	segments, err := sealedSegments(basePath)
	if err != nil {
		return 0, "", err
	}
	_, segments = partitionArchivedSegments(checkpoint, hasCheckpoint, segments)
	activeInfo, err := os.Lstat(basePath)
	if err != nil {
		if os.IsNotExist(err) && len(segments) == 0 && !hasCheckpoint {
			return 0, "", nil
		}
		if os.IsNotExist(err) {
			return 0, "", errors.New("active audit segment is missing")
		}
		return 0, "", err
	}
	if !activeInfo.Mode().IsRegular() {
		return 0, "", errors.New("active audit segment is not a regular file")
	}
	paths := make([]string, 0, len(segments)+1)
	for _, segment := range segments {
		paths = append(paths, segment.path)
	}
	paths = append(paths, basePath)

	var total int64
	expectSeq := int64(1)
	prev := ""
	if hasCheckpoint {
		total = checkpoint.ArchivedThroughSeq
		expectSeq = checkpoint.ArchivedThroughSeq + 1
		prev = checkpoint.ArchivedHead
	}
	for i, path := range paths {
		nextSeq, head, count, err := verifyChainFile(path, expectSeq, prev)
		if err != nil {
			return total, prev, err
		}
		if i < len(segments) && nextSeq != segments[i].order {
			return total, prev, fmt.Errorf("%s: sealed segment suffix=%d, expected %d", path, segments[i].order, nextSeq)
		}
		expectSeq = nextSeq
		prev = head
		total += count
	}
	return total, prev, nil
}

func verifyChainFile(path string, expectSeq int64, prev string) (int64, string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return expectSeq, prev, 0, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var count int64
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			_ = f.Close()
			return expectSeq, prev, count, fmt.Errorf("%s record %d: corrupt JSON: %w", path, count+1, err)
		}
		if rec.Seq != expectSeq {
			_ = f.Close()
			return expectSeq, prev, count, fmt.Errorf("%s: seq=%d, expected %d", path, rec.Seq, expectSeq)
		}
		if rec.PrevHash != prev {
			_ = f.Close()
			return expectSeq, prev, count, fmt.Errorf("%s seq %d: prev_hash mismatch", path, rec.Seq)
		}
		if got := hashOf(rec); got != rec.Hash {
			_ = f.Close()
			return expectSeq, prev, count, fmt.Errorf("%s seq %d: hash mismatch", path, rec.Seq)
		}
		prev = rec.Hash
		expectSeq++
		count++
	}
	scanErr := sc.Err()
	closeErr := f.Close()
	if scanErr != nil {
		return expectSeq, prev, count, scanErr
	}
	if closeErr != nil {
		return expectSeq, prev, count, closeErr
	}
	return expectSeq, prev, count, nil
}

const maxAuditTailRecords = 10_000
const maxAuditTailBytes = 4 << 20

// Tail returns up to the last n records across the active and rotated segments.
func (l *Logger) Tail(n int) ([]Record, error) {
	if n <= 0 || n > maxAuditTailRecords {
		return nil, fmt.Errorf("audit tail count must be in 1..%d", maxAuditTailRecords)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	segments, err := sealedSegments(l.path)
	if err != nil {
		return nil, err
	}
	checkpoint, hasCheckpoint, err := loadArchiveCheckpoint(l.path)
	if err != nil {
		return nil, err
	}
	_, segments = partitionArchivedSegments(checkpoint, hasCheckpoint, segments)
	paths := make([]string, 0, len(segments)+1)
	paths = append(paths, l.path)
	for i := len(segments) - 1; i >= 0; i-- {
		paths = append(paths, segments[i].path)
	}

	var out []Record
	remainingBytes := maxAuditTailBytes
	for _, path := range paths {
		part, used, err := tailSegment(path, n-len(out), remainingBytes)
		if err != nil {
			return nil, err
		}
		out = append(part, out...)
		remainingBytes -= used
		if len(out) >= n || remainingBytes <= 0 {
			break
		}
	}
	return out, nil
}

func tailSegment(path string, n, maxBytes int) ([]Record, int, error) {
	if n <= 0 || maxBytes <= 0 {
		return nil, 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lines := make([][]byte, 0, min(n, 256))
	totalBytes := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		copyLine := append([]byte(nil), line...)
		lines = append(lines, copyLine)
		totalBytes += len(copyLine)
		for len(lines) > n || totalBytes > maxBytes {
			totalBytes -= len(lines[0])
			lines[0] = nil
			lines = lines[1:]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	out := make([]Record, len(lines))
	for i := range lines {
		if err := json.Unmarshal(lines[i], &out[i]); err != nil {
			return nil, 0, err
		}
	}
	return out, totalBytes, nil
}
