package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultRetainedSegments  = 64
	archiveCheckpointSuffix  = ".checkpoint"
	archiveCheckpointVersion = 1
	maxCheckpointBytes       = 64 << 10
)

// archiveCheckpoint is a bounded commitment to the deliberately pruned prefix.
// ArchiveRoot folds the exact bytes and sequence boundaries of every removed
// segment. ArchivedHead anchors the first retained record to that prefix.
type archiveCheckpoint struct {
	Version              int       `json:"version"`
	CreatedAt            time.Time `json:"created_at"`
	ArchivedThroughSeq   int64     `json:"archived_through_seq"`
	ArchivedHead         string    `json:"archived_head"`
	ArchiveRoot          string    `json:"archive_root"`
	ArchivedSegmentCount int64     `json:"archived_segment_count"`
	Hash                 string    `json:"hash"`
}

type archivedSegmentCommitment struct {
	PreviousRoot string `json:"previous_root"`
	Order        int64  `json:"order"`
	FirstSeq     int64  `json:"first_seq"`
	LastSeq      int64  `json:"last_seq"`
	Head         string `json:"head"`
	FileSHA256   string `json:"file_sha256"`
	Bytes        int64  `json:"bytes"`
	Records      int64  `json:"records"`
}

func (l *Logger) retainedSegmentLimit() int {
	if l.maxSegments > 0 {
		return l.maxSegments
	}
	return defaultRetainedSegments
}

func checkpointPath(basePath string) string {
	return basePath + archiveCheckpointSuffix
}

func checkpointHash(checkpoint archiveCheckpoint) string {
	checkpoint.Hash = ""
	encoded, _ := json.Marshal(checkpoint)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func loadArchiveCheckpoint(basePath string) (archiveCheckpoint, bool, error) {
	path := checkpointPath(basePath)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return archiveCheckpoint{}, false, nil
		}
		return archiveCheckpoint{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCheckpointBytes {
		return archiveCheckpoint{}, false, fmt.Errorf("invalid audit checkpoint file %q", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return archiveCheckpoint{}, false, err
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxCheckpointBytes+1))
	decoder.DisallowUnknownFields()
	var checkpoint archiveCheckpoint
	decodeErr := decoder.Decode(&checkpoint)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	closeErr := f.Close()
	if decodeErr != nil {
		return archiveCheckpoint{}, false, fmt.Errorf("decode audit checkpoint: %w", decodeErr)
	}
	if !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			trailingErr = errors.New("trailing JSON value")
		}
		return archiveCheckpoint{}, false, fmt.Errorf("decode audit checkpoint: %w", trailingErr)
	}
	if closeErr != nil {
		return archiveCheckpoint{}, false, closeErr
	}
	if checkpoint.Version != archiveCheckpointVersion || checkpoint.CreatedAt.IsZero() ||
		checkpoint.ArchivedThroughSeq <= 0 || checkpoint.ArchivedThroughSeq == int64(^uint64(0)>>1) ||
		checkpoint.ArchivedSegmentCount <= 0 || !validSHA256(checkpoint.ArchivedHead) ||
		!validSHA256(checkpoint.ArchiveRoot) || !validSHA256(checkpoint.Hash) ||
		checkpointHash(checkpoint) != checkpoint.Hash {
		return archiveCheckpoint{}, false, errors.New("invalid audit archive checkpoint")
	}
	return checkpoint, true, nil
}

func writeArchiveCheckpoint(basePath string, checkpoint archiveCheckpoint) error {
	checkpoint.Hash = checkpointHash(checkpoint)
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	dir := filepath.Dir(basePath)
	tmp, err := os.CreateTemp(dir, ".audit-checkpoint-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, checkpointPath(basePath)); err != nil {
		return err
	}
	committed = true
	return syncDirectory(dir)
}

func partitionArchivedSegments(checkpoint archiveCheckpoint, hasCheckpoint bool, segments []sealedSegment) ([]sealedSegment, []sealedSegment) {
	if !hasCheckpoint {
		return nil, segments
	}
	boundary := checkpoint.ArchivedThroughSeq + 1
	index := 0
	for index < len(segments) && segments[index].order <= boundary {
		index++
	}
	return segments[:index], segments[index:]
}

func (l *Logger) enforceRetentionLocked() error {
	checkpoint, hasCheckpoint, err := loadArchiveCheckpoint(l.path)
	if err != nil {
		return err
	}
	segments, err := sealedSegments(l.path)
	if err != nil {
		return err
	}
	stale, retained := partitionArchivedSegments(checkpoint, hasCheckpoint, segments)
	limit := l.retainedSegmentLimit()
	pruneCount := len(retained) - limit
	if pruneCount <= 0 {
		return removeArchivedSegments(filepath.Dir(l.path), stale)
	}

	expectSeq := int64(1)
	previousHead := ""
	archiveRoot := ""
	archivedSegments := int64(0)
	if hasCheckpoint {
		expectSeq = checkpoint.ArchivedThroughSeq + 1
		previousHead = checkpoint.ArchivedHead
		archiveRoot = checkpoint.ArchiveRoot
		archivedSegments = checkpoint.ArchivedSegmentCount
	}

	for _, segment := range retained[:pruneCount] {
		firstSeq := expectSeq
		nextSeq, head, records, err := verifyChainFile(segment.path, expectSeq, previousHead)
		if err != nil {
			return fmt.Errorf("checkpoint audit segment: %w", err)
		}
		if records == 0 || nextSeq != segment.order {
			return fmt.Errorf("checkpoint audit segment %q: suffix=%d, chain ends at %d", segment.path, segment.order, nextSeq)
		}
		fileHash, size, err := hashFile(segment.path)
		if err != nil {
			return err
		}
		commitment := archivedSegmentCommitment{
			PreviousRoot: archiveRoot,
			Order:        segment.order,
			FirstSeq:     firstSeq,
			LastSeq:      nextSeq - 1,
			Head:         head,
			FileSHA256:   fileHash,
			Bytes:        size,
			Records:      records,
		}
		encoded, _ := json.Marshal(commitment)
		sum := sha256.Sum256(encoded)
		archiveRoot = hex.EncodeToString(sum[:])
		expectSeq = nextSeq
		previousHead = head
		archivedSegments++
	}

	checkpoint = archiveCheckpoint{
		Version:              archiveCheckpointVersion,
		CreatedAt:            time.Now().UTC(),
		ArchivedThroughSeq:   expectSeq - 1,
		ArchivedHead:         previousHead,
		ArchiveRoot:          archiveRoot,
		ArchivedSegmentCount: archivedSegments,
	}
	if err := writeArchiveCheckpoint(l.path, checkpoint); err != nil {
		return fmt.Errorf("write audit checkpoint: %w", err)
	}
	remove := append(append([]sealedSegment(nil), stale...), retained[:pruneCount]...)
	return removeArchivedSegments(filepath.Dir(l.path), remove)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", n, copyErr
	}
	if closeErr != nil {
		return "", n, closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func removeArchivedSegments(dir string, segments []sealedSegment) error {
	if len(segments) == 0 {
		return nil
	}
	for _, segment := range segments {
		if err := os.Remove(segment.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
