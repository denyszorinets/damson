package sqlite

import (
	"net/url"
	"path/filepath"
	"strings"
	"sync"
)

// migrateLocks serializes migrations per database file within this process.
// The map is bounded by the number of distinct database files opened and is
// never pruned.
var migrateLocks = struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

// lockMigrations blocks until the migration lock for key is held and returns
// the unlock function. It ignores ctx on purpose: migrations are short, so
// waiting is bounded.
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
// a DSN (a plain path or a file: URI), so every Store on one file shares a
// lock. Query parameters are stripped and percent-escapes in file: URIs are
// decoded. Case-insensitive filesystems are not normalized: two spellings that
// differ only in case get different keys there.
func canonicalPath(dsn string) string {
	p := dsn
	if strings.HasPrefix(dsn, "file:") {
		if u, err := url.Parse(dsn); err == nil {
			p = u.Path
			if u.Opaque != "" {
				if d, err := url.PathUnescape(u.Opaque); err == nil {
					p = d
				} else {
					p = u.Opaque
				}
			}
		} else {
			p = strings.TrimPrefix(dsn, "file:")
			if i := strings.IndexByte(p, '?'); i >= 0 {
				p = p[:i]
			}
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}
