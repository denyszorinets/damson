package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/denyszorinets/damson/internal/config"
	"github.com/denyszorinets/damson/internal/secrets"
	secretsdb "github.com/denyszorinets/damson/internal/secrets/db"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/sqlite"
)

// devKEKPath is where `damson dev` keeps its generated key: next to the SQLite
// file, so removing the database files resets the key together with the data.
func devKEKPath(cfg config.Config) string {
	return sqlite.FilePath(cfg.Database.DSN) + ".kek"
}

// ensureDevKEK gives the dev role a key when none is configured: it reads
// <db file>.kek, or generates it (0600). A configured key (DAMSON_KEK or
// secrets.kek_file) always wins. Only the dev role calls this; a controller
// never generates a key, because several would each make their own. Returns the
// key file path when it is the key in use.
func ensureDevKEK(ctx context.Context, st secretsdb.Settings, cfg *config.Config, log *slog.Logger) (string, error) {
	path := devKEKPath(*cfg)
	if cfg.Secrets.KEK != "" {
		if _, err := os.Stat(path); err == nil {
			log.Warn("a dev key file exists but a configured KEK takes precedence; the file is ignored", "path", path)
		}
		return "", nil
	}
	key, err := readDevKEK(path)
	if errors.Is(err, fs.ErrNotExist) {
		// Never make a key for a database that already holds secrets: it would fit
		// nothing and hide that the real key file is missing.
		if _, cerr := st.GetSetting(ctx, secretsdb.CanaryKey); cerr == nil {
			if key, err = readDevKEK(path); err != nil { // a concurrent dev process may just have made it
				db := sqlite.FilePath(cfg.Database.DSN)
				return "", fmt.Errorf("%w: this database holds encrypted secrets but its dev key file %s is missing: restore it, or delete %s, %s-wal, %s-shm and %s to start over",
					secretsdb.ErrKEK, path, db, db, db, path)
			}
		} else if !errors.Is(cerr, store.ErrNotFound) {
			return "", fmt.Errorf("reading the KEK canary: %w", cerr)
		} else if key, err = createDevKEK(path); errors.Is(err, fs.ErrExist) {
			key, err = readDevKEK(path) // another dev process won the race
		}
	}
	if err != nil {
		return "", fmt.Errorf("dev key file %s: %w", path, err)
	}
	raw, _ := secrets.ParseKEK(key) // validated by readDevKEK
	log.Warn("dev secrets are encrypted with a generated key; losing this file loses them", "path", path, "kek_id", secrets.KeyID(raw))
	cfg.Secrets.KEK = key
	return path, nil
}

// readDevKEK reads and checks an existing key file: a regular file, private to
// the owner, holding a valid key.
func readDevKEK(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	// Check what was opened, not what the path named a moment ago.
	fi2, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(fi, fi2) {
		return "", errors.New("changed while being read")
	}
	if fi2.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("permissions %04o are too open; run: chmod 600 %s", fi2.Mode().Perm(), path)
	}
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if _, err := secrets.ParseKEK(key); err != nil {
		return "", errors.New("does not hold a valid key (empty or damaged); delete it together with the database to start over")
	}
	return key, nil
}

// createDevKEK writes a new key without ever leaving a half-written or empty
// file at path: it fills a private temporary file in the same directory, syncs
// it, then links it into place, which fails if the file already exists.
func createDevKEK(path string) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	defer clear(raw)
	key := base64.StdEncoding.EncodeToString(raw)
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*") // created 0600, named so a damson.db* delete catches it
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(key + "\n"); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return "", err
	}
	// Make the new directory entry durable before the canary is committed.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return key, nil
}
