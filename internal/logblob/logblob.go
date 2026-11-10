// Package logblob holds the byte stores that back log chunk objects
// (ARCHITECTURE section 10). The chunk manifest names the backend of every
// chunk, so a Set can read old chunks after the write backend changes.
package logblob

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

// Backend names recorded in log_chunks.backend.
const (
	BackendDB = "db"
	BackendS3 = "s3"
)

// ErrUnknownBackend is returned for a chunk whose backend is not configured.
var ErrUnknownBackend = errors.New("log backend is not configured")

// Blob stores chunk objects by key.
type Blob interface {
	// Put stores (or overwrites) the bytes for key.
	Put(ctx context.Context, key string, data []byte) error
	// Get returns an error wrapping store.ErrNotFound if the key is absent.
	Get(ctx context.Context, key string) ([]byte, error)
	// DeletePrefix removes every key starting with prefix and returns how many.
	// An empty prefix is store.ErrInvalid.
	DeletePrefix(ctx context.Context, prefix string) (int64, error)
}

// Set is the configured backends and the one new chunks are written to.
type Set struct {
	write string
	m     map[string]Blob
}

// NewSet returns a Set writing to write. The db backend must be among blobs.
func NewSet(write string, blobs map[string]Blob) (*Set, error) {
	if blobs[write] == nil {
		return nil, fmt.Errorf("log write backend %q is not configured: %w", write, ErrUnknownBackend)
	}
	m := make(map[string]Blob, len(blobs))
	for k, v := range blobs {
		m[k] = v
	}
	return &Set{write: write, m: m}, nil
}

// Writer returns the backend name and store for new chunks.
func (s *Set) Writer() (string, Blob) { return s.write, s.m[s.write] }

// Get reads key from the named backend.
func (s *Set) Get(ctx context.Context, backend, key string) ([]byte, error) {
	b := s.m[backend]
	if b == nil {
		return nil, fmt.Errorf("backend %q: %w", backend, ErrUnknownBackend)
	}
	return b.Get(ctx, key)
}

// Prober is implemented by backends that can check they are reachable.
type Prober interface {
	Probe(ctx context.Context) error
}

// Probe checks every external backend that can be probed. The db backend is
// covered by the database ping.
func (s *Set) Probe(ctx context.Context) error {
	for _, b := range s.External() {
		if p, ok := b.(Prober); ok {
			if err := p.Probe(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// External returns every configured backend except db, in name order. Their
// objects are deleted by the sweeper, not in a database transaction.
func (s *Set) External() []Blob {
	names := make([]string, 0, len(s.m))
	for n := range s.m {
		if n != BackendDB {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]Blob, 0, len(names))
	for _, n := range names {
		out = append(out, s.m[n])
	}
	return out
}

// DB stores chunk objects in the database through store.LogStore.
type DB struct {
	S   store.LogStore
	Now func() time.Time
}

// Put implements Blob.
func (d DB) Put(ctx context.Context, key string, data []byte) error {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	return d.S.PutLogData(ctx, key, data, now())
}

// Get implements Blob.
func (d DB) Get(ctx context.Context, key string) ([]byte, error) { return d.S.GetLogData(ctx, key) }

// DeletePrefix implements Blob.
func (d DB) DeletePrefix(ctx context.Context, prefix string) (int64, error) {
	return d.S.DeleteLogDataPrefix(ctx, prefix)
}

// DBOnly returns a Set with the db backend only.
func DBOnly(s store.LogStore) *Set {
	set, _ := NewSet(BackendDB, map[string]Blob{BackendDB: DB{S: s}})
	return set
}
