package transfer

import (
	"context"
	"io"
)

// fileInfo is what a transfer needs to know about a path.
type fileInfo struct {
	Size  int64
	IsDir bool
}

// fileSystem is one side of a transfer. Paths are absolute and slash
// separated. A protocol implements it once and serves as source or
// destination.
type fileSystem interface {
	// Stat reports whether path exists. A missing path is (zero, false, nil).
	Stat(ctx context.Context, path string) (fileInfo, bool, error)
	// Open reads a file.
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// Create makes a new file; it fails if the path exists.
	Create(ctx context.Context, path string) (io.WriteCloser, error)
	// Mkdirs creates dir and its missing parents.
	Mkdirs(ctx context.Context, dir string) error
	// Rename moves from to to. Without overwrite it fails if to exists (the
	// check is atomic where the protocol allows it); with it, it replaces to.
	Rename(ctx context.Context, from, to string, overwrite bool) error
	// Remove deletes a file.
	Remove(ctx context.Context, path string) error
	// Close releases the connection. It may be called while an operation is
	// blocked, to abort it.
	Close() error
}
