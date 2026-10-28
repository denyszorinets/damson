package store

import (
	"context"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

// LogChunk is a manifest row pointing at a stored log object (ARCHITECTURE
// section 10). Backend names the LogStore holding ObjectKey.
type LogChunk struct {
	ID          string
	ExecutionID string
	SeqFrom     int64
	SeqTo       int64
	Backend     string
	ObjectKey   string
	Bytes       int64
	TsFirst     time.Time
	TsLast      time.Time
	CreatedAt   time.Time
}

// NewLogChunk describes a chunk to record.
type NewLogChunk struct {
	ExecutionID string
	SeqFrom     int64
	SeqTo       int64
	Backend     string
	ObjectKey   string
	Bytes       int64
	TsFirst     time.Time
	TsLast      time.Time
}

// LogStore persists the log chunk manifest and the object bytes of the
// default "db" LogStore backend.
type LogStore interface {
	// InsertLogChunk records a chunk, fenced by attemptID: it is applied only
	// if attemptID is the execution's current attempt. It returns ErrNotFound
	// if the execution is absent, ErrStale for a stale attempt (nothing is
	// written), ErrConflict if a chunk already starts at SeqFrom, and
	// ErrInvalid for an inverted range or empty backend or key.
	InsertLogChunk(ctx context.Context, attemptID string, c NewLogChunk, now time.Time) (LogChunk, error)
	// ListLogChunks returns the chunks overlapping [fromSeq, toSeq] in
	// sequence order, at most limit. A toSeq <= 0 means unbounded.
	ListLogChunks(ctx context.Context, executionID string, fromSeq, toSeq int64, limit int) ([]LogChunk, error)

	// PutLogData stores (or overwrites) the bytes for key.
	PutLogData(ctx context.Context, key string, data []byte, now time.Time) error
	// GetLogData returns ErrNotFound if the key is absent.
	GetLogData(ctx context.Context, key string) ([]byte, error)
	// DeleteLogDataPrefix removes every key starting with prefix and returns
	// how many it removed. An empty prefix is ErrInvalid.
	DeleteLogDataPrefix(ctx context.Context, prefix string) (int64, error)
}

// LogDataPrefix is the key prefix under which every log object of an
// execution is stored (the chunk writer names objects logs/<execution>/...).
// Claiming an execution under a new attempt deletes everything below it:
// sequence numbers restart for each attempt, so the previous attempt's chunks
// must not mix with the new one's.
func LogDataPrefix(executionID string) string { return "logs/" + executionID + "/" }

// ValidateLogChunk rejects chunks the store cannot record.
func ValidateLogChunk(attemptID string, c NewLogChunk) error {
	if attemptID == "" || c.ExecutionID == "" || c.Backend == "" || c.ObjectKey == "" {
		return fmt.Errorf("log chunk needs attempt id, execution id, backend and key: %w", ErrInvalid)
	}
	if c.SeqFrom < 0 || c.SeqTo < c.SeqFrom || c.Bytes < 0 {
		return fmt.Errorf("log chunk range %d-%d / %d bytes: %w", c.SeqFrom, c.SeqTo, c.Bytes, ErrInvalid)
	}
	return nil
}

// LogRange returns toSeq, or MaxInt64 when it is unbounded (<= 0).
func LogRange(toSeq int64) int64 {
	if toSeq <= 0 {
		return math.MaxInt64
	}
	return toSeq
}

// PrefixArgs validates a delete prefix and returns its length in characters,
// the unit SQL substr counts in on both backends.
func PrefixArgs(prefix string) (int, error) {
	if prefix == "" {
		return 0, fmt.Errorf("empty log data prefix: %w", ErrInvalid)
	}
	return utf8.RuneCountInString(prefix), nil
}
