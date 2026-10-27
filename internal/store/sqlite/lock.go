package sqlite

import (
	"path/filepath"
	"strings"
	"sync"
)

// migrateLocks serializes migrations per database file within this process.
var migrateLocks = struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

// lockMigrations blocks until the migration lock for key is held and returns
// the unlock function.
func lockMigrations(key string) func() {
	migrateLocks.mu.Lock()
	l, ok := migrateLocks.m[key]
	if !ok {
		l = &sync.Mutex{}
		migrateLocks.m[key] = l
	}
	migrateLocks.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// canonicalPath returns the absolute, symlink-resolved database file path from
// a DSN (a path or a file: URI), so every Store on one file shares a lock.
func canonicalPath(dsn string) string {
	p := strings.TrimPrefix(dsn, "file:")
	p = strings.TrimPrefix(p, "//")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}
